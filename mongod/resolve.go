package mongod

import (
	"bytes"
	"encoding/hex"
	"errors"
	"net"
	"os"
	"slices"
	"strings"
)

// envHostIP names the environment variable that overrides address
// resolution. It exists for CI configuration that cannot pass options.
const envHostIP = "MONGOTEST_HOST_IP"

// loopback is the address a container's published port is reachable at when
// the daemon and this process share a network namespace.
const loopback = "127.0.0.1"

// allInterfaces is the bind address for a container that has to be reachable
// from somewhere other than the daemon host's own loopback. See bindIP.
const allInterfaces = "0.0.0.0"

// errNoDefaultRoute reports that the routing table has no default route with
// a gateway, which is the state of a container attached only to an internal
// network.
var errNoDefaultRoute = errors.New("no default route with a gateway")

// procNetRoute and netClassDir are the two places this namespace's interfaces
// can be read from, in that order of authority.
//
// /proc/net/route always describes the reading process's own namespace, so
// when it can be read it settles the question by itself: a bridge that is up
// is a bridge with a route to its own subnet. /sys/class/net is tagged with
// whichever namespace sysfs was mounted from instead -- the container's in
// every ordinary case, but the host's for a container that bind-mounts /sys,
// where it would report the host's bridges to a process that cannot reach
// them. It is therefore a fallback for having no routing table at all, and
// not a second opinion about one that was read.
const (
	procNetRoute = "/proc/net/route"
	netClassDir  = "/sys/class/net"
)

// runtimeBridgePrefixes name the interfaces a container runtime creates on
// the machine it runs on: docker0 is Docker's default bridge, docker_gwbridge
// is a swarm node's, br-<network id> is a user-defined network's, and podman
// names its own after itself.
//
// None of them can exist inside a container that has a network namespace of
// its own, because a runtime puts nothing but a veth and loopback in one.
// Seeing one therefore means this namespace is the daemon host's.
var runtimeBridgePrefixes = []string{"docker", "br-", "podman", "cni-podman"}

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
		if !slices.Contains(networkFiles, mountPoint) {
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
	// bridge reports a container runtime's bridge visible from this network
	// namespace, which is how a container sharing the daemon host's namespace
	// is told apart from one that has its own.
	bridge func() (string, bool)
}

// hostAddress is what address resolution produces: where to dial, and what
// decided it.
//
// The two travel together because four of the six rows infer the address
// rather than being told it, and an inferred address that is wrong fails as a
// readiness timeout on a port that is listening somewhere else. That failure
// is only diagnosable if the error can say which row answered, so the reason
// has to survive as far as NotReadyError.
type hostAddress struct {
	// Host is the address to dial and to put in a URI.
	Host string
	// Source names what decided it, phrased to be read inside "the address
	// came from ...".
	Source string
}

// resolve returns the host to dial, most specific source first: the option,
// then the environment variable, then the daemon address.
func (r hostResolver) resolve() (hostAddress, error) {
	if r.override != "" {
		return hostAddress{Host: r.override, Source: "WithHostIP"}, nil
	}
	if fromEnv := r.getenv(envHostIP); fromEnv != "" {
		return hostAddress{Host: fromEnv, Source: envHostIP}, nil
	}

	scheme, hostPart := splitDockerHost(r.dockerHost)
	switch scheme {
	case "tcp", "http", "https", "ssh":
		// A published port lands on the machine running the daemon.
		return hostAddress{
			Host:   remoteHost(hostPart),
			Source: "the daemon address " + r.dockerHost,
		}, nil
	default:
		// A unix socket or a named pipe reaches a daemon on this machine, and
		// an unrecognised transport (a test double, say) is treated the same
		// way: loopback is the safe guess, and WithHostIP corrects it.
		return r.localHost()
	}
}

// localHost answers for a daemon on this machine. Whether loopback is right
// depends on whether this process shares that machine's network namespace,
// which being in a container does not by itself decide.
func (r hostResolver) localHost() (hostAddress, error) {
	containerisation := r.detect()
	if !containerisation.Containerised {
		return hostAddress{
			Host:   loopback,
			Source: "loopback, because the daemon is on this machine and this process is not in a container",
		}, nil
	}
	if bridge, found := r.visibleBridge(); found {
		// Containerised, but not in a network namespace of its own: this is
		// docker run --network host, a runner with network_mode: host, or a
		// pod with hostNetwork: true, each with the daemon's socket mounted.
		// The daemon publishes into this very namespace, so loopback is
		// right and the default route here is the physical network's router,
		// which nothing has published anything on.
		return hostAddress{
			Host:   loopback,
			Source: "loopback, because " + bridge + " is visible from here, so this process shares the daemon host's network namespace",
		}, nil
	}
	// A sibling container's port is published on the daemon's host, which is
	// a different namespace from this one. The host is reachable at the
	// default route's gateway, which is the bridge address.
	gateway, err := r.gateway()
	if err != nil {
		return hostAddress{}, &UnresolvedHostError{DockerHost: r.dockerHost, Signal: containerisation.Signal, Err: err}
	}
	return hostAddress{
		Host:   gateway,
		Source: "the default route's gateway, because this process is in a network namespace of its own (" + containerisation.Signal + ")",
	}, nil
}

// visibleBridge asks for this namespace's runtime bridge, tolerating a
// resolver built without the lookup.
func (r hostResolver) visibleBridge() (string, bool) {
	if r.bridge == nil {
		return "", false
	}
	return r.bridge()
}

// realBridge reports a container runtime bridge visible from this process's
// network namespace.
func realBridge() (string, bool) { return runtimeBridge(readFileOS, listDirOS) }

// runtimeBridge returns the first container runtime bridge this namespace can
// see, and whether there was one. See procNetRoute for why the routing table
// answers alone whenever it can be read.
//
// Nothing is inferred from not finding one: a /proc and a /sys that neither
// can be read are not evidence that this namespace belongs to a container, so
// the caller falls back to the answer it would have given anyway.
func runtimeBridge(readFile func(string) ([]byte, error), listDir func(string) ([]string, error)) (string, bool) {
	if routes, err := readFile(procNetRoute); err == nil {
		for line := range bytes.Lines(routes) {
			// Iface is the first column; the header's own "Iface" matches
			// nothing below, so it needs no special case.
			fields := bytes.Fields(line)
			if len(fields) == 0 {
				continue
			}
			if name := string(fields[0]); isRuntimeBridge(name) {
				return name, true
			}
		}
		return "", false
	}
	names, err := listDir(netClassDir)
	if err != nil {
		return "", false
	}
	for _, name := range names {
		if isRuntimeBridge(name) {
			return name, true
		}
	}
	return "", false
}

// isRuntimeBridge reports whether an interface name is one a container runtime
// gives a bridge of its own. See runtimeBridgePrefixes.
func isRuntimeBridge(name string) bool {
	for _, prefix := range runtimeBridgePrefixes {
		if strings.HasPrefix(name, prefix) {
			return true
		}
	}
	return false
}

// listDirOS is the real directory listing, handed in so that every case above
// can be tested from a table without needing the namespace it describes.
func listDirOS(dir string) ([]string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		names = append(names, entry.Name())
	}
	return names, nil
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
