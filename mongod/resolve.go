package mongod

import (
	"bytes"
	"encoding/hex"
	"errors"
	"net"
	"os"
	"strings"
)

// envHostIP names the environment variable that overrides address
// resolution. It exists for CI configuration that cannot pass options.
const envHostIP = "MONGOTEST_HOST_IP"

// loopback is the address a container's published port is reachable at when
// the daemon and this process share a network namespace.
const loopback = "127.0.0.1"

// errNoDefaultRoute reports that the routing table has no default route with
// a gateway, which is the state of a container attached only to an internal
// network.
var errNoDefaultRoute = errors.New("no default route with a gateway")

// Containerisation reports whether this process is itself running inside a
// container, and what that conclusion was based on. The signal is part of the
// result because the conclusion decides where every container is dialled: a
// wrong one has to be diagnosable, not merely overridable.
type Containerisation struct {
	// Containerised is true when this process appears to be inside a
	// container.
	Containerised bool
	// Signal names what decided it, in words that make sense in a log line or
	// an error message.
	Signal string
}

// DetectContainerisation reports whether this process is running inside a
// container, by looking for the marks a container runtime leaves on the
// filesystem. It reads only /proc and two marker files, and never contacts
// the daemon.
func DetectContainerisation() Containerisation {
	return detectContainerisation(existsOS, readFileOS)
}

// existsOS and readFileOS are the real filesystem lookups. Detection takes
// them as arguments so that every case below can be tested from a table
// without needing the container it describes.
func existsOS(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

func readFileOS(path string) ([]byte, error) { return os.ReadFile(path) }

// runtimeTokens are the names container runtimes leave in a cgroup path.
var runtimeTokens = []string{"docker", "containerd", "kubepods", "libpod"}

// networkFiles are the three files a runtime bind-mounts into a container
// from its own state directory. Nothing on a host mounts them, which makes
// them a signal that survives a cgroup namespace that hides everything else.
var networkFiles = []string{"/etc/resolv.conf", "/etc/hostname", "/etc/hosts"}

func detectContainerisation(exists func(string) bool, readFile func(string) ([]byte, error)) Containerisation {
	if exists("/.dockerenv") {
		return Containerisation{Containerised: true, Signal: "/.dockerenv exists, which Docker creates in every container"}
	}
	if exists("/run/.containerenv") {
		return Containerisation{Containerised: true, Signal: "/run/.containerenv exists, which Podman creates in every container"}
	}
	if token, ok := cgroupRuntime(readFile); ok {
		return Containerisation{Containerised: true, Signal: "/proc/self/cgroup names " + token}
	}
	if ok := mountedNetworkFiles(readFile); ok {
		return Containerisation{Containerised: true, Signal: "/proc/self/mountinfo shows the network files bind-mounted in by a container runtime"}
	}
	return Containerisation{Signal: "none of the container marker files, cgroup paths or bind mounts were present"}
}

// cgroupRuntime reports a container runtime named in this process's cgroup
// path. Under a private cgroup namespace the path is just "/" and says
// nothing, which is why it is not the only signal.
func cgroupRuntime(readFile func(string) ([]byte, error)) (string, bool) {
	content, err := readFile("/proc/self/cgroup")
	if err != nil {
		return "", false
	}
	for line := range strings.Lines(string(content)) {
		// Each line is hierarchy:controllers:path; only the path can name a
		// runtime, and matching the whole line would hit a controller called
		// "devices" on a name that happens to contain a token.
		_, path, found := strings.Cut(strings.TrimSpace(line), ":")
		if !found {
			continue
		}
		if _, path, found = strings.Cut(path, ":"); !found {
			continue
		}
		for _, token := range runtimeTokens {
			if strings.Contains(path, token) {
				return token, true
			}
		}
	}
	return "", false
}

// mountedNetworkFiles reports whether /etc/resolv.conf and friends are bind
// mounts from a container runtime's state directory.
//
// The mount point is what is checked, not the source: a host running Docker
// has /var/lib/docker paths all over its own mount table (an overlay rootfs
// and a shm tmpfs for every running container), and reading those as being
// inside a container would send every developer laptop to the bridge gateway.
func mountedNetworkFiles(readFile func(string) ([]byte, error)) bool {
	content, err := readFile("/proc/self/mountinfo")
	if err != nil {
		return false
	}
	for line := range strings.Lines(string(content)) {
		// Fields are: id parent major:minor root mountpoint options...
		fields := strings.Fields(line)
		if len(fields) < 5 {
			continue
		}
		root, mountPoint := fields[3], fields[4]
		if !slicesContains(networkFiles, mountPoint) {
			continue
		}
		for _, token := range runtimeTokens {
			if strings.Contains(root, token) {
				return true
			}
		}
	}
	return false
}

func slicesContains(haystack []string, needle string) bool {
	for _, s := range haystack {
		if s == needle {
			return true
		}
	}
	return false
}

// hostResolver works out the address a caller can reach a container's
// published port at. Where the daemon is decides the answer, so the daemon
// address is the input and not a constant.
//
// The lookups are fields rather than direct calls so that the rule can be
// tested as a table, without the machine running the test deciding the
// outcome.
type hostResolver struct {
	// dockerHost is what the client reported for Host(), in DOCKER_HOST form.
	dockerHost string
	// override is the address WithHostIP supplied, or "".
	override string
	// getenv reads the environment, for MONGOTEST_HOST_IP.
	getenv func(string) string
	// detect reports whether this process is itself containerised.
	detect func() Containerisation
	// gateway reports the default route's gateway.
	gateway func() (string, error)
}

// resolve returns the host to dial, most specific source first: the option,
// then the environment variable, then the daemon address.
func (r hostResolver) resolve() (string, error) {
	if r.override != "" {
		return r.override, nil
	}
	if fromEnv := r.getenv(envHostIP); fromEnv != "" {
		return fromEnv, nil
	}

	scheme, hostPart := splitDockerHost(r.dockerHost)
	switch scheme {
	case "tcp", "http", "https", "ssh":
		// A published port lands on the machine running the daemon.
		return remoteHost(hostPart), nil
	default:
		// A unix socket or a named pipe reaches a daemon on this machine, and
		// an unrecognised transport (a test double, say) is treated the same
		// way: loopback is the safe guess, and WithHostIP corrects it.
		return r.localHost()
	}
}

// localHost answers for a daemon on this machine. Whether loopback is right
// depends on whether this process shares that machine's network namespace.
func (r hostResolver) localHost() (string, error) {
	containerisation := r.detect()
	if !containerisation.Containerised {
		return loopback, nil
	}
	// A sibling container's port is published on the daemon's host, which is
	// a different namespace from this one. The host is reachable at the
	// default route's gateway, which is the bridge address.
	gateway, err := r.gateway()
	if err != nil {
		return "", &UnresolvedHostError{DockerHost: r.dockerHost, Signal: containerisation.Signal, Err: err}
	}
	return gateway, nil
}

// splitDockerHost separates the scheme from the rest of a DOCKER_HOST value.
// A value with no scheme has no host to read either, so both come back empty.
func splitDockerHost(dockerHost string) (scheme, hostPart string) {
	scheme, rest, found := strings.Cut(dockerHost, "://")
	if !found {
		return "", ""
	}
	return strings.ToLower(scheme), rest
}

// remoteHost pulls the hostname out of the rest of a daemon address, dropping
// an ssh user and a port. A daemon bound to a wildcard address is on this
// machine: 0.0.0.0 is what a daemon listens on, never something to dial.
func remoteHost(hostPart string) string {
	if _, after, found := strings.Cut(hostPart, "@"); found {
		hostPart = after
	}
	// The path of an http:// daemon address is not part of the host.
	hostPart, _, _ = strings.Cut(hostPart, "/")
	host := hostPart
	if h, _, err := net.SplitHostPort(hostPart); err == nil {
		host = h
	} else if trimmed, ok := strings.CutPrefix(hostPart, "["); ok {
		// An ipv6 address with no port still carries its brackets.
		host, _, _ = strings.Cut(trimmed, "]")
	}
	if host == "" {
		return loopback
	}
	if ip := net.ParseIP(host); ip != nil && ip.IsUnspecified() {
		return loopback
	}
	return host
}

// realGateway reads the default route's gateway from the kernel.
func realGateway() (string, error) { return defaultGateway(readFileOS) }

// defaultGateway reads /proc/net/route and returns the gateway of the default
// route. It is Linux-only, which is the only place a process can be a sibling
// of the containers it starts through a bind-mounted socket.
func defaultGateway(readFile func(string) ([]byte, error)) (string, error) {
	content, err := readFile("/proc/net/route")
	if err != nil {
		return "", err
	}
	return parseDefaultGateway(content)
}

// parseDefaultGateway pulls the gateway address out of the kernel's ipv4
// routing table.
//
// The columns are Iface, Destination, Gateway and then flags and metrics. The
// default route is the one whose destination is all zeroes. Both addresses
// are written as little-endian hex, so 010011AC is 172.17.0.1 and reading the
// bytes in file order would give 1.0.17.172 instead.
func parseDefaultGateway(routes []byte) (string, error) {
	for line := range bytes.Lines(routes) {
		fields := bytes.Fields(line)
		if len(fields) < 3 {
			continue
		}
		destination, ok := parseLittleEndianIPv4(fields[1])
		if !ok || !destination.IsUnspecified() {
			continue
		}
		gateway, ok := parseLittleEndianIPv4(fields[2])
		if !ok || gateway.IsUnspecified() {
			// A default route on a point-to-point link has no gateway, and
			// 0.0.0.0 is not an address anything can be dialled at.
			continue
		}
		return gateway.String(), nil
	}
	return "", errNoDefaultRoute
}

// parseLittleEndianIPv4 decodes one of the kernel's routing table addresses.
func parseLittleEndianIPv4(field []byte) (net.IP, bool) {
	const ipv4HexDigits = 8
	if len(field) != ipv4HexDigits {
		return nil, false
	}
	raw, err := hex.AppendDecode(nil, field)
	if err != nil {
		return nil, false
	}
	return net.IPv4(raw[3], raw[2], raw[1], raw[0]), true
}
