package mongod

import (
	"errors"
	"io/fs"
	"net"
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
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := tc.resolver.resolve()

			require.NoError(t, err, "this row resolves to an address, so it must not report a failure")
			assert.Equal(t, tc.want, got, tc.why)
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
