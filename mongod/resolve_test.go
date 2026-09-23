package mongod

import (
	"errors"
	"io/fs"
	"net"
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/tophergopher/mongotest/dockerclient"
)

// notContainerised and containerisedIn stand in for the detection, so that
// the resolution table below is about the rule and not about what the machine
// running the test happens to be.
func notContainerised() Containerisation {
	return Containerisation{Signal: "none of the container signals were present"}
}

func containerisedIn(signal string) func() Containerisation {
	return func() Containerisation { return Containerisation{Containerised: true, Signal: signal} }
}

func noGateway() (string, error) {
	return "", errNoDefaultRoute
}

func gatewayAt(ip string) func() (string, error) {
	return func() (string, error) { return ip, nil }
}

// bridgeVisible stands in for a container runtime's bridge being visible from
// this network namespace, which is what separates a container with a namespace
// of its own from one sharing the daemon host's.
func bridgeVisible(iface string) func() (string, bool) {
	return func() (string, bool) { return iface, true }
}

// noBridge is a namespace with no runtime bridge in it, which is what a lookup
// that could read nothing also produces.
func noBridge() (string, bool) { return "", false }

func noEnv(string) string { return "" }

func envHostIPSetTo(value string) func(string) string {
	return func(key string) string {
		if key == envHostIP {
			return value
		}
		return ""
	}
}

func TestResolveHostTable(t *testing.T) {
	cases := []struct {
		name     string
		resolver hostResolver
		want     string
		why      string
	}{
		{
			name:     "a unix socket outside a container is loopback",
			resolver: hostResolver{dockerHost: "unix:///var/run/docker.sock", getenv: noEnv, detect: notContainerised, gateway: noGateway},
			want:     "127.0.0.1",
			why:      "the daemon publishes on this machine's loopback and this process shares its network namespace, which is the developer laptop case",
		},
		{
			name:     "a tcp daemon publishes on its own host",
			resolver: hostResolver{dockerHost: "tcp://build-host:2376", getenv: noEnv, detect: notContainerised, gateway: noGateway},
			want:     "build-host",
			why:      "a published port lands on the machine running the daemon, so a remote daemon means a remote address",
		},
		{
			name:     "an http daemon host is read the same way",
			resolver: hostResolver{dockerHost: "http://build-host:2375", getenv: noEnv, detect: notContainerised, gateway: noGateway},
			want:     "build-host",
			why:      "http:// is the same endpoint spelled differently, so it resolves the same way",
		},
		{
			name:     "an https daemon host is read the same way",
			resolver: hostResolver{dockerHost: "https://build-host:2376", getenv: noEnv, detect: notContainerised, gateway: noGateway},
			want:     "build-host",
			why:      "https:// is the same endpoint with TLS, so it resolves the same way",
		},
		{
			name:     "an ssh daemon host drops the user",
			resolver: hostResolver{dockerHost: "ssh://deploy@build-host", getenv: noEnv, detect: notContainerised, gateway: noGateway},
			want:     "build-host",
			why:      "the user is an ssh detail and dialling deploy@build-host would fail, so only the hostname is the address",
		},
		{
			name:     "a wildcard tcp bind means this machine",
			resolver: hostResolver{dockerHost: "tcp://0.0.0.0:2375", getenv: noEnv, detect: notContainerised, gateway: noGateway},
			want:     "127.0.0.1",
			why:      "0.0.0.0 is what a daemon binds to, never an address to dial, and the only machine it can mean from here is this one",
		},
		{
			name:     "the ipv6 wildcard means this machine too",
			resolver: hostResolver{dockerHost: "tcp://[::]:2375", getenv: noEnv, detect: notContainerised, gateway: noGateway},
			want:     "127.0.0.1",
			why:      ":: is the same wildcard in its ipv6 spelling",
		},
		{
			name:     "a named pipe is local",
			resolver: hostResolver{dockerHost: `npipe:////./pipe/docker_engine`, getenv: noEnv, detect: notContainerised, gateway: noGateway},
			want:     "127.0.0.1",
			why:      "a named pipe reaches a daemon on this machine, exactly as a unix socket does",
		},
		{
			name:     "a transport we do not recognise is treated as local",
			resolver: hostResolver{dockerHost: "fake://dockermock", getenv: noEnv, detect: notContainerised, gateway: noGateway},
			want:     "127.0.0.1",
			why:      "the test doubles report a scheme of their own, and loopback is both the safe guess and what WithHostIP exists to correct",
		},
		{
			name:     "an empty daemon host is treated as local",
			resolver: hostResolver{dockerHost: "", getenv: noEnv, detect: notContainerised, gateway: noGateway},
			want:     "127.0.0.1",
			why:      "there is nothing to read a hostname out of, and guessing anything else would send the caller somewhere wrong",
		},
		{
			name:     "a unix socket inside a container is the default gateway",
			resolver: hostResolver{dockerHost: "unix:///var/run/docker.sock", getenv: noEnv, detect: containerisedIn("/.dockerenv exists"), gateway: gatewayAt("172.17.0.1")},
			want:     "172.17.0.1",
			why:      "the sibling container's port is published on the host's loopback, which is a different namespace from this one; the host is reachable at the bridge gateway",
		},
		{
			name:     "the environment variable beats detection",
			resolver: hostResolver{dockerHost: "unix:///var/run/docker.sock", getenv: envHostIPSetTo("10.1.2.3"), detect: containerisedIn("/.dockerenv exists"), gateway: gatewayAt("172.17.0.1")},
			want:     "10.1.2.3",
			why:      "MONGOTEST_HOST_IP is how a CI job that cannot pass options corrects a topology detection got wrong",
		},
		{
			name:     "the environment variable beats a tcp daemon host too",
			resolver: hostResolver{dockerHost: "tcp://build-host:2376", getenv: envHostIPSetTo("10.1.2.3"), detect: notContainerised, gateway: noGateway},
			want:     "10.1.2.3",
			why:      "an operator who set the variable knows something the daemon address does not say, such as a NAT in front of the build host",
		},
		{
			name:     "the option beats the environment variable",
			resolver: hostResolver{dockerHost: "tcp://build-host:2376", override: "10.9.9.9", getenv: envHostIPSetTo("10.1.2.3"), detect: notContainerised, gateway: noGateway},
			want:     "10.9.9.9",
			why:      "WithHostIP is the most specific statement of intent there is, so nothing outranks it",
		},
		{
			name: "a container on the host's network namespace is loopback",
			resolver: hostResolver{
				dockerHost: "unix:///var/run/docker.sock", getenv: noEnv,
				detect:  containerisedIn("/.dockerenv exists"),
				gateway: gatewayAt("192.168.2.1"),
				bridge:  bridgeVisible("docker0"),
			},
			want: "127.0.0.1",
			why:  "docker run --network host is containerised but shares the daemon host's network namespace, so the published port is on this namespace's loopback; the default route here is the physical network's router, which is measurably not listening (measured on Docker 29.3.1: loopback answers, the gateway refuses)",
		},
		{
			name: "a kubernetes pod with hostNetwork is loopback",
			resolver: hostResolver{
				dockerHost: "unix:///var/run/docker.sock", getenv: noEnv,
				detect:  containerisedIn("/proc/self/cgroup names kubepods"),
				gateway: gatewayAt("10.0.0.1"),
				bridge:  bridgeVisible("docker0"),
			},
			want: "127.0.0.1",
			why:  "a pod with hostNetwork: true and the node's socket mounted is the same shape as --network host, and it is reached the same way",
		},
		{
			name: "podman's bridge says the same thing docker0 does",
			resolver: hostResolver{
				dockerHost: "unix:///run/podman/podman.sock", getenv: noEnv,
				detect:  containerisedIn("/run/.containerenv exists"),
				gateway: gatewayAt("192.168.2.1"),
				bridge:  bridgeVisible("cni-podman0"),
			},
			want: "127.0.0.1",
			why:  "the question is whether this namespace is the daemon's, and podman names its bridge differently without changing the answer",
		},
		{
			name: "a user-defined bridge is enough on its own",
			resolver: hostResolver{
				dockerHost: "unix:///var/run/docker.sock", getenv: noEnv,
				detect:  containerisedIn("/.dockerenv exists"),
				gateway: gatewayAt("192.168.2.1"),
				bridge:  bridgeVisible("br-9e1a2b3c4d5e"),
			},
			want: "127.0.0.1",
			why:  "a daemon whose networks are all user-defined has no docker0 to find, and br-<id> is the name it gives those bridges instead",
		},
		{
			name: "a container with its own namespace still takes the gateway",
			resolver: hostResolver{
				dockerHost: "unix:///var/run/docker.sock", getenv: noEnv,
				detect:  containerisedIn("/.dockerenv exists"),
				gateway: gatewayAt("172.17.0.1"),
				bridge:  noBridge,
			},
			want: "172.17.0.1",
			why:  "a bridge-networked container sees only its own veth and loopback, which is the sibling-container case the gateway is for; measured inside one, /sys/class/net holds exactly eth0 and lo",
		},
		{
			name: "a namespace that cannot be read falls back to the gateway",
			resolver: hostResolver{
				dockerHost: "unix:///var/run/docker.sock", getenv: noEnv,
				detect:  containerisedIn("/.dockerenv exists"),
				gateway: gatewayAt("172.17.0.1"),
				bridge:  noBridge,
			},
			want: "172.17.0.1",
			why:  "an unreadable /sys is no evidence that this namespace is the daemon's, and the sibling-container answer is the one that was there before this check existed",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := tc.resolver.resolve()

			require.NoError(t, err, "this row resolves to an address, so it must not report a failure")
			assert.Equal(t, tc.want, got.Host, tc.why)
			assert.NotEmpty(t, got.Source, "every row has to say what decided it: a probe that times out on an inferred address is only diagnosable if the error can name where the address came from")
		})
	}
}

func TestResolveHostFailsActionablyInsideAContainerWithNoGateway(t *testing.T) {
	r := hostResolver{
		dockerHost: "unix:///var/run/docker.sock",
		getenv:     noEnv,
		detect:     containerisedIn("/.dockerenv exists"),
		gateway:    noGateway,
	}

	_, err := r.resolve()

	require.Error(t, err, "guessing loopback here is the failure this whole resolution exists to prevent: the container starts, the daemon reports a port, and the connection times out with nothing to explain why")
	assert.ErrorIs(t, err, ErrUnresolvedHost, "callers branch on the sentinel rather than on the message")
	assert.ErrorIs(t, err, dockerclient.ErrInvalidArgument, "there is nothing wrong with the daemon; what is missing is a setting only the caller can supply")
	assert.Contains(t, err.Error(), envHostIP, "the reader cannot act on this without being told the environment variable that fixes it")
	assert.Contains(t, err.Error(), "WithHostIP", "the option is the other half of the escape hatch and belongs in the same message")
	assert.Contains(t, err.Error(), "/.dockerenv exists", "the signal that decided this process is containerised is what the reader checks first when the conclusion is wrong")
}

func TestDetectContainerisationTable(t *testing.T) {
	// A host that is running Docker: containers of its own are mounted under
	// /var/lib/docker, which must not be mistaken for being inside one.
	hostMountinfo := []byte(
		"25 30 0:22 / /sys rw,nosuid,nodev,noexec shared:7 - sysfs sysfs rw\n" +
			"612 30 0:52 / /var/lib/docker/containers/9e1a/mounts/shm rw,nosuid,nodev,noexec shared:1 - tmpfs shm rw,size=65536k\n" +
			"620 30 0:53 / /var/lib/docker/overlay2/1f2e/merged rw,relatime shared:2 - overlay overlay rw\n")
	// The same file inside a container: the runtime bind-mounts the three
	// network files in from its own state directory.
	containerMountinfo := []byte(
		"1234 1200 0:60 / / rw,relatime - overlay overlay rw,lowerdir=/var/lib/docker/overlay2/l/AAA\n" +
			"1240 1234 8:1 /var/lib/docker/containers/9e1a/resolv.conf /etc/resolv.conf rw,relatime - ext4 /dev/sda1 rw\n" +
			"1241 1234 8:1 /var/lib/docker/containers/9e1a/hostname /etc/hostname rw,relatime - ext4 /dev/sda1 rw\n")

	cases := []struct {
		name  string
		files map[string]string
		want  bool
		why   string
	}{
		{
			name:  "docker writes a marker file",
			files: map[string]string{"/.dockerenv": ""},
			want:  true,
			why:   "/.dockerenv exists in every Docker container and nowhere else, which makes it the cheapest and most reliable signal there is",
		},
		{
			name:  "podman writes its own marker file",
			files: map[string]string{"/run/.containerenv": "engine=\"podman-5.3.1\"\n"},
			want:  true,
			why:   "Podman writes /run/.containerenv where Docker writes /.dockerenv, and mongotest supports both runtimes",
		},
		{
			name:  "a kubernetes pod is named in the cgroup path",
			files: map[string]string{"/proc/self/cgroup": "0::/kubepods/besteffort/pod9e1a/8f2c\n"},
			want:  true,
			why:   "a pod's containers carry no marker file, so the cgroup path is what identifies them",
		},
		{
			name:  "a docker scope is named in the cgroup path",
			files: map[string]string{"/proc/self/cgroup": "0::/system.slice/docker-9e1a.scope\n"},
			want:  true,
			why:   "with the host cgroup namespace shared into the container the path still names the runtime",
		},
		{
			name:  "podman names libpod in the cgroup path",
			files: map[string]string{"/proc/self/cgroup": "0::/machine.slice/libpod-9e1a.scope\n"},
			want:  true,
			why:   "libpod is Podman's own spelling of the same thing",
		},
		{
			name:  "the bind-mounted network files give it away",
			files: map[string]string{"/proc/self/mountinfo": string(containerMountinfo)},
			want:  true,
			why:   "the runtime bind-mounts /etc/resolv.conf and /etc/hostname in from its state directory, which nothing on a host does",
		},
		{
			name:  "a plain host is not containerised",
			files: map[string]string{"/proc/self/cgroup": "0::/user.slice/user-1000.slice/session-3.scope\n"},
			want:  false,
			why:   "an ordinary login session on a laptop must resolve to loopback, which is the case that has to keep working",
		},
		{
			name:  "a host running docker is still not containerised",
			files: map[string]string{"/proc/self/cgroup": "0::/user.slice/user-1000.slice/session-3.scope\n", "/proc/self/mountinfo": string(hostMountinfo)},
			want:  false,
			why:   "a host's mount table names /var/lib/docker for every container it runs; reading that as being inside one would send every laptop to the bridge gateway",
		},
		{
			name:  "no signals at all",
			files: map[string]string{},
			want:  false,
			why:   "with nothing to go on the answer is the case that is both far more common and recoverable with WithHostIP",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			exists := func(path string) bool { _, ok := tc.files[path]; return ok }
			read := func(path string) ([]byte, error) {
				content, ok := tc.files[path]
				if !ok {
					return nil, fs.ErrNotExist
				}
				return []byte(content), nil
			}

			got := detectContainerisation(exists, read)

			assert.Equal(t, tc.want, got.Containerised, tc.why)
			assert.NotEmpty(t, got.Signal, "the result records why it decided what it did, so a wrong answer can be argued with rather than just worked around")
		})
	}
}

func TestParseDefaultGateway(t *testing.T) {
	// The kernel writes the address little-endian, so 010211AC is 172.17.0.1.
	const routes = "Iface\tDestination\tGateway \tFlags\tRefCnt\tUse\tMetric\tMask\t\tMTU\tWindow\tIRTT\n" +
		"eth0\t000011AC\t00000000\t0001\t0\t0\t0\t0000FFFF\t0\t0\t0\n" +
		"eth0\t00000000\t010011AC\t0003\t0\t0\t0\t00000000\t0\t0\t0\n"

	got, err := parseDefaultGateway([]byte(routes))

	require.NoError(t, err, "the table has a default route, so a gateway must be found")
	assert.Equal(t, "172.17.0.1", got, "the kernel writes the address little-endian, so reading it in file order would give 172.17.0.1 back as 1.0.17.172")
}

func TestParseDefaultGatewayWithoutADefaultRoute(t *testing.T) {
	const routes = "Iface\tDestination\tGateway \tFlags\tRefCnt\tUse\tMetric\tMask\t\tMTU\tWindow\tIRTT\n" +
		"eth0\t000011AC\t00000000\t0001\t0\t0\t0\t0000FFFF\t0\t0\t0\n"

	_, err := parseDefaultGateway([]byte(routes))

	assert.ErrorIs(t, err, errNoDefaultRoute, "a container on an internal network with no route out has no gateway to offer, and saying so is what turns into an actionable error higher up")
}

func TestParseDefaultGatewayIgnoresARouteWithNoGateway(t *testing.T) {
	// A default route on a point-to-point link has no gateway address.
	const routes = "Iface\tDestination\tGateway \tFlags\tRefCnt\tUse\tMetric\tMask\t\tMTU\tWindow\tIRTT\n" +
		"tun0\t00000000\t00000000\t0001\t0\t0\t0\t00000000\t0\t0\t0\n"

	_, err := parseDefaultGateway([]byte(routes))

	assert.ErrorIs(t, err, errNoDefaultRoute, "an all-zero gateway is the absence of one, and 0.0.0.0 is not an address anything can be dialled at")
}

func TestParseDefaultGatewayOnRealProcNetRoute(t *testing.T) {
	// Whatever machine this runs on, a gateway that parses must be a usable
	// unicast address; this guards the parser against the real file's layout
	// drifting from the fixture above.
	got, err := defaultGateway(readFileOS)
	if err != nil {
		assert.ErrorIs(t, err, errNoDefaultRoute, "the only expected failure on a real machine is having no default route; anything else means the parser broke")
		return
	}

	ip := net.ParseIP(got)
	require.NotNil(t, ip, "whatever the parser returns is handed straight to net.Dial, so it has to be an address")
	assert.False(t, ip.IsUnspecified(), "0.0.0.0 is the absence of a gateway and must have been rejected rather than returned")
}

func FuzzParseDefaultGateway(f *testing.F) {
	f.Add([]byte("Iface\tDestination\tGateway\neth0\t00000000\t010011AC\t0003\t0\t0\t0\t00000000\t0\t0\t0\n"))
	f.Add([]byte("eth0\t00000000\t00000000\n"))
	f.Add([]byte(""))
	f.Add([]byte("\n\n\n"))
	f.Add([]byte("eth0\tZZZZZZZZ\tZZZZZZZZ\n"))

	f.Fuzz(func(t *testing.T, routes []byte) {
		got, err := parseDefaultGateway(routes)

		if err != nil {
			require.True(t, errors.Is(err, errNoDefaultRoute), "the parser reads a kernel file that no attacker controls, so the only failure it may invent is the absence of a route")
			return
		}
		ip := net.ParseIP(got)
		require.NotNil(t, ip, "anything the parser accepts is dialled, so it must be a parseable address for every input")
		require.NotNil(t, ip.To4(), "the routing table this reads is the ipv4 one, so a result that is not an ipv4 address means the parser lost track of what it was reading")
		require.False(t, ip.IsUnspecified(), "0.0.0.0 means there is no gateway, so it must never be returned as one")
	})
}

// The address an inferred row produces is the one that goes wrong, so each
// row has to be able to say what produced it. See NotReadyError.HostSource.
func TestResolveHostNamesWhatDecidedTheAddress(t *testing.T) {
	cases := []struct {
		name     string
		resolver hostResolver
		contains string
		why      string
	}{
		{
			name:     "the option names itself",
			resolver: hostResolver{dockerHost: "unix:///var/run/docker.sock", override: "10.9.9.9", getenv: noEnv, detect: notContainerised, gateway: noGateway},
			contains: "WithHostIP",
			why:      "a caller who set the address and still cannot reach it needs to be told their own setting is what was used",
		},
		{
			name:     "the environment variable names itself",
			resolver: hostResolver{dockerHost: "unix:///var/run/docker.sock", getenv: envHostIPSetTo("10.1.2.3"), detect: notContainerised, gateway: noGateway},
			contains: envHostIP,
			why:      "CI sets this variable in a file nobody is looking at while the test fails, so the failure has to name it",
		},
		{
			name:     "a remote daemon names the daemon address",
			resolver: hostResolver{dockerHost: "tcp://build-host:2376", getenv: noEnv, detect: notContainerised, gateway: noGateway},
			contains: "tcp://build-host:2376",
			why:      "the address came from DOCKER_HOST, and that is where the reader has to go to change it",
		},
		{
			name: "the host network case names the bridge it found",
			resolver: hostResolver{
				dockerHost: "unix:///var/run/docker.sock", getenv: noEnv,
				detect:  containerisedIn("/.dockerenv exists"),
				gateway: gatewayAt("192.168.2.1"),
				bridge:  bridgeVisible("docker0"),
			},
			contains: "docker0",
			why:      "this row overrides a containerised process's usual answer, so the evidence for it has to be arguable rather than only overridable",
		},
		{
			name: "the sibling container case names the containerisation signal",
			resolver: hostResolver{
				dockerHost: "unix:///var/run/docker.sock", getenv: noEnv,
				detect:  containerisedIn("/.dockerenv exists"),
				gateway: gatewayAt("172.17.0.1"),
				bridge:  noBridge,
			},
			contains: "/.dockerenv exists",
			why:      "a wrong containerisation verdict is what sends a process to the gateway, so the verdict travels with the address",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := tc.resolver.resolve()

			require.NoError(t, err, "these rows all resolve")
			assert.Contains(t, got.Source, tc.contains, tc.why)
		})
	}
}

func TestRuntimeBridgeTable(t *testing.T) {
	const header = "Iface\tDestination\tGateway \tFlags\tRefCnt\tUse\tMetric\tMask\t\tMTU\tWindow\tIRTT\n"
	// Captured from a host running Docker, and from inside a container on its
	// default bridge, on Docker 29.3.1.
	const hostRoutes = header +
		"eth0\t00000000\t010200C0\t0003\t0\t0\t0\t00000000\t0\t0\t0\n" +
		"docker0\t000011AC\t00000000\t0001\t0\t0\t0\t0000FFFF\t0\t0\t0\n"
	const containerRoutes = header +
		"eth0\t00000000\t010011AC\t0003\t0\t0\t0\t00000000\t0\t0\t0\n" +
		"eth0\t000011AC\t00000000\t0001\t0\t0\t0\t0000FFFF\t0\t0\t0\n"

	cases := []struct {
		name       string
		routes     string
		interfaces []string
		unreadable bool
		want       string
		found      bool
		why        string
	}{
		{
			name: "docker's default bridge has a route", routes: hostRoutes, interfaces: []string{"docker0", "eth0", "lo"},
			want: "docker0", found: true,
			why: "a namespace with a route on docker0 is the daemon host's own, which is what --network host gives a container; this is what /proc/net/route holds inside one",
		},
		{
			name: "a container's own namespace has neither", routes: containerRoutes, interfaces: []string{"eth0", "lo"},
			found: false,
			why:   "a container on a bridge sees only its own veth and loopback, which is the sibling-container case the gateway rule is for; this is what both sources hold inside one",
		},
		{
			name:       "a user-defined network's bridge counts",
			routes:     header + "br-9e1a2b3c4d5e\t000012AC\t00000000\t0001\t0\t0\t0\t0000FFFF\t0\t0\t0\n",
			interfaces: []string{"br-9e1a2b3c4d5e", "lo"},
			want:       "br-9e1a2b3c4d5e", found: true,
			why: "a daemon whose networks are all user-defined has no docker0 to find, and br-<id> is the name it gives those bridges instead",
		},
		{
			name:       "a swarm node's gateway bridge counts",
			routes:     header + "docker_gwbridge\t000013AC\t00000000\t0001\t0\t0\t0\t0000FFFF\t0\t0\t0\n",
			interfaces: []string{"docker_gwbridge", "lo"},
			want:       "docker_gwbridge", found: true,
			why: "a swarm node has this instead of, or as well as, docker0, and it is just as much the daemon's",
		},
		{
			name: "a readable routing table settles it", routes: containerRoutes, interfaces: []string{"docker0", "eth0", "lo"},
			found: false,
			why:   "sysfs is tagged with whichever namespace it was mounted from, so a bridge container that bind-mounts the host's /sys sees the host's bridges there; the routing table is this namespace's either way, and mistaking that container for the host's namespace would send a sibling to its own loopback",
		},
		{
			name: "no routing table falls through to sysfs", interfaces: []string{"cni-podman0", "lo"},
			want: "cni-podman0", found: true,
			why: "somewhere without /proc mounted, the interface list is the only thing left to ask, and it is right far more often than it is wrong",
		},
		{
			name: "a bridge only in the routing table is still found", routes: hostRoutes, interfaces: []string{"eth0", "lo"},
			want: "docker0", found: true,
			why: "the routing table is read for its own sake and not as a hint towards the interface list, which may be describing another namespace entirely",
		},
		{
			name: "neither source can be read", unreadable: true, found: false,
			why: "reading nothing is not evidence that this namespace belongs to a container, so the gateway rule stays in charge -- which is the answer that was there before this check existed",
		},
		{
			name: "a name that merely starts like one", routes: containerRoutes, interfaces: []string{"brain0", "lo"},
			found: false,
			why:   "br- is the prefix the daemon uses, and matching br alone would claim any interface someone named after a brain or a bridgehead",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			readFile := func(path string) ([]byte, error) {
				if path != procNetRoute || tc.routes == "" {
					return nil, fs.ErrNotExist
				}
				return []byte(tc.routes), nil
			}
			listDir := func(path string) ([]string, error) {
				if path != netClassDir || tc.unreadable {
					return nil, fs.ErrNotExist
				}
				return tc.interfaces, nil
			}

			got, found := runtimeBridge(readFile, listDir)

			assert.Equal(t, tc.found, found, tc.why)
			assert.Equal(t, tc.want, got, tc.why)
		})
	}
}

// The fakes above decide what this process looks like, which means none of
// them can catch either lookup reading the wrong path. This one does.
func TestTheRealLookupsReadThisNamespace(t *testing.T) {
	if _, err := os.Stat(netClassDir); err != nil {
		t.Skipf("%s is not present here, and the lookup answers nothing rather than guessing: %v", netClassDir, err)
	}

	interfaces, err := listDirOS(netClassDir)

	require.NoError(t, err, "the directory exists, so listing it has to succeed; a failure here reads as \"cannot tell\" and would quietly send every containerised process to the gateway")
	assert.Contains(t, interfaces, "lo", "every network namespace has a loopback interface, so not finding one means this is reading something other than the interface list")

	routes, err := readFileOS(procNetRoute)

	require.NoError(t, err, "the routing table is the first source runtimeBridge asks, and it is on every Linux kernel")
	assert.Contains(t, string(routes), "Iface", "and it has to be the routing table rather than whatever else might sit at that path")
}
