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

// errNoDefaultRoute reports a routing table with no default gateway, as a
// container on an internal-only network has.
var errNoDefaultRoute = errors.New("the routing table has no default route with a gateway")

// Where this process's network interfaces can be read from. The routing
// table always describes this process's own network namespace. /sys can be
// the host's if it is bind-mounted, so it is only a fallback.
const (
	procNetRoute = "/proc/net/route"
	netClassDir  = "/sys/class/net"
)

// runtimeBridgePrefixes start the names of the bridges Docker and Podman
// create on the machine they run on. A container with its own network has
// only a veth and loopback, so seeing a bridge means this is the daemon
// host's network.
var runtimeBridgePrefixes = []string{"docker", "br-", "podman", "cni-podman"}

// containerisation says whether this process is in a container, and what
// showed it, so that a wrong conclusion can be diagnosed.
type containerisation struct {
	containerised bool
	// evidence says what decided it, in words fit for an error message.
	evidence string
}

// detectContainerisation looks for the marker file Docker or Podman creates
// in every container. A runtime that leaves neither is not detected; set
// MONGOTEST_HOST_IP there.
func detectContainerisation() containerisation {
	return containerisationFrom(existsOS)
}

// containerisationFrom is detectContainerisation over any filesystem, so a
// test can describe a machine.
func containerisationFrom(exists func(string) bool) containerisation {
	if exists("/.dockerenv") {
		return containerisation{containerised: true, evidence: "/.dockerenv exists, which Docker creates in every container"}
	}
	if exists("/run/.containerenv") {
		return containerisation{containerised: true, evidence: "/run/.containerenv exists, which Podman creates in every container"}
	}
	return containerisation{evidence: "neither /.dockerenv nor /run/.containerenv exists"}
}

// The real filesystem lookups. The functions above take them as arguments
// so tests can describe any machine.
func existsOS(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

func readFileOS(path string) ([]byte, error) { return os.ReadFile(path) }

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

// hostResolver works out the address a container's published port can be
// reached at. The lookups are fields so the rule can be tested as a table.
type hostResolver struct {
	// dockerHost is the daemon address, in DOCKER_HOST form.
	dockerHost string
	// override is the WithHostIP address, or "".
	override string
	getenv   func(string) string
	// detect reports whether this process is in a container.
	detect func() containerisation
	// gateway returns the default route's gateway.
	gateway func() (string, error)
	// bridge returns a runtime bridge visible from this process, if any.
	bridge func() (string, bool)
}

// hostResolver returns the address resolution for these settings and the
// daemon address the client reported.
func (c config) hostResolver(dockerHost string) hostResolver {
	return hostResolver{
		dockerHost: dockerHost,
		override:   c.hostIP,
		getenv:     os.Getenv,
		detect:     detectContainerisation,
		gateway:    realGateway,
		bridge:     realBridge,
	}
}

// hostAddress is a resolved address and what decided it. The source is kept
// for the error a readiness timeout returns: a wrong address looks just like
// a container that never came up.
type hostAddress struct {
	Host string
	// Source reads naturally after "the address came from".
	Source string
}

// resolve returns the host to dial: the option, then the environment
// variable, then wherever the daemon is.
func (r hostResolver) resolve() (hostAddress, error) {
	if r.override != "" {
		return hostAddress{Host: r.override, Source: "WithHostIP"}, nil
	}
	if fromEnv := r.getenv(EnvHostIP); fromEnv != "" {
		return hostAddress{Host: fromEnv, Source: EnvHostIP}, nil
	}
	scheme, hostPart := splitDockerHost(r.dockerHost)
	switch scheme {
	case "tcp", "http", "https", "ssh":
		// A port is published on the machine the daemon runs on.
		return hostAddress{Host: remoteHost(hostPart), Source: "the daemon address " + r.dockerHost}, nil
	default:
		// A unix socket or named pipe, or a test double: the daemon is local.
		return r.localHost()
	}
}

// localHost answers for a daemon on this machine. Loopback is right unless
// this process is in a container with a network of its own.
func (r hostResolver) localHost() (hostAddress, error) {
	inContainer := r.detect()
	if !inContainer.containerised {
		return hostAddress{
			Host:   loopback,
			Source: "loopback, because the daemon is on this machine and this process is not in a container",
		}, nil
	}
	// A container on the host's network, as --network host gives, shares
	// the namespace the port is published into.
	if bridge, found := r.visibleBridge(); found {
		return hostAddress{
			Host:   loopback,
			Source: "loopback, because " + bridge + " is visible from here, so this process shares the daemon host's network namespace",
		}, nil
	}
	// Otherwise the port is on the host outside, which is the gateway.
	gateway, err := r.gateway()
	if err != nil {
		return hostAddress{}, &UnresolvedHostError{DockerHost: r.dockerHost, Evidence: inContainer.evidence, Err: err}
	}
	return hostAddress{
		Host:   gateway,
		Source: "the default route's gateway, because this process is in a network namespace of its own (" + inContainer.evidence + ")",
	}, nil
}

// visibleBridge calls the bridge lookup, if the resolver has one. Start
// always supplies it; a test that builds a resolver by hand may not.
func (r hostResolver) visibleBridge() (string, bool) {
	if r.bridge == nil {
		return "", false
	}
	return r.bridge()
}

// realBridge is the bridge lookup against this machine.
func realBridge() (string, bool) { return runtimeBridge(readFileOS, listDirOS) }

// runtimeBridge returns a container runtime's bridge visible from this
// process. If the routing table can be read, it alone decides; /sys is
// consulted only when it cannot. See procNetRoute.
func runtimeBridge(readFile func(string) ([]byte, error), listDir func(string) ([]string, error)) (string, bool) {
	names, err := routeInterfaces(readFile)
	if err != nil {
		names, err = listDir(netClassDir)
	}
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

// routeInterfaces returns the interface of every route in the routing table.
func routeInterfaces(readFile func(string) ([]byte, error)) ([]string, error) {
	routes, err := readFile(procNetRoute)
	if err != nil {
		return nil, err
	}
	var names []string
	for line := range bytes.Lines(routes) {
		// The interface is the first column. The header's "Iface" is kept:
		// it matches no bridge name.
		if fields := bytes.Fields(line); len(fields) > 0 {
			names = append(names, string(fields[0]))
		}
	}
	return names, nil
}

// isRuntimeBridge reports whether an interface is a container runtime's
// bridge. See runtimeBridgePrefixes.
func isRuntimeBridge(name string) bool {
	return slices.ContainsFunc(runtimeBridgePrefixes, func(prefix string) bool {
		return strings.HasPrefix(name, prefix)
	})
}

// splitDockerHost separates the scheme from the rest of a DOCKER_HOST value.
// Both are empty if there is no scheme.
func splitDockerHost(dockerHost string) (scheme, hostPart string) {
	scheme, rest, found := strings.Cut(dockerHost, "://")
	if !found {
		return "", ""
	}
	return strings.ToLower(scheme), rest
}

// remoteHost returns the hostname from a daemon address, without any user,
// port or path. A wildcard such as 0.0.0.0 is what a daemon listens on, not
// somewhere to dial, so it means this machine.
func remoteHost(hostPart string) string {
	if _, afterUser, found := strings.Cut(hostPart, "@"); found {
		hostPart = afterUser
	}
	hostPart, _, _ = strings.Cut(hostPart, "/")
	host := stripPort(hostPart)
	if host == "" || isWildcard(host) {
		return loopback
	}
	return host
}

// isWildcard reports whether a host is the unspecified address, 0.0.0.0 or ::.
func isWildcard(host string) bool {
	ip := net.ParseIP(host)
	return ip != nil && ip.IsUnspecified()
}

// stripPort removes a port, and the brackets around an IPv6 address.
func stripPort(hostPort string) string {
	if host, _, err := net.SplitHostPort(hostPort); err == nil {
		return host
	}
	return strings.Trim(hostPort, "[]")
}

// realGateway is the gateway lookup against this machine.
func realGateway() (string, error) { return defaultGateway(readFileOS) }

// defaultGateway returns the default route's gateway. It reads /proc, so it
// is Linux-only. So is the case that needs it: a process in a container next
// to the ones it starts.
func defaultGateway(readFile func(string) ([]byte, error)) (string, error) {
	content, err := readFile(procNetRoute)
	if err != nil {
		return "", err
	}
	return parseDefaultGateway(content)
}

// parseDefaultGateway finds the gateway of the default route, which is the
// route whose destination is all zeroes. The columns are Iface, Destination,
// Gateway.
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
		// A point-to-point default route has no gateway to dial.
		gateway, ok := parseLittleEndianIPv4(fields[2])
		if !ok || gateway.IsUnspecified() {
			continue
		}
		return gateway.String(), nil
	}
	return "", errNoDefaultRoute
}

// parseLittleEndianIPv4 decodes an address as the routing table writes it:
// eight hex digits, least significant byte first, so 010011AC is 172.17.0.1.
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
