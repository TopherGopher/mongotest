package mongod

import (
	"errors"
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// These tests are inside the package because where a container is reachable
// depends on the machine: whether this process is in a container, and what
// its routing table says. The resolver takes those as lookups so every case
// can be stated here, whatever machine runs the tests.

func noEnv(string) string { return "" }

func onAHost() containerisation { return containerisation{evidence: "no marker file"} }

func inAContainer() containerisation {
	return containerisation{containerised: true, evidence: "/.dockerenv exists"}
}

func gatewayAt(address string) func() (string, error) {
	return func() (string, error) { return address, nil }
}

func noGateway() (string, error) { return "", errNoDefaultRoute }

func bridgeVisible() (string, bool) { return "docker0", true }

func noBridge() (string, bool) { return "", false }

func TestWhereAContainerIsReached(t *testing.T) {
	const socket = "unix:///var/run/docker.sock"
	cases := []struct {
		name     string
		resolver hostResolver
		want     string
	}{
		{"WithHostIP wins over everything",
			hostResolver{dockerHost: "tcp://build-host:2376", override: "10.0.0.5", getenv: func(string) string { return "10.9.9.9" }}, "10.0.0.5"},
		{"then the environment variable",
			hostResolver{dockerHost: "tcp://build-host:2376", getenv: func(string) string { return "10.9.9.9" }}, "10.9.9.9"},
		{"a remote daemon publishes on its own host",
			hostResolver{dockerHost: "tcp://build-host:2376", getenv: noEnv}, "build-host"},
		{"an ssh daemon, without the user",
			hostResolver{dockerHost: "ssh://deploy@build-host", getenv: noEnv}, "build-host"},
		{"an http daemon, without the path",
			hostResolver{dockerHost: "http://build-host:2375/v1", getenv: noEnv}, "build-host"},
		{"an IPv6 daemon, without the brackets",
			hostResolver{dockerHost: "tcp://[fd00::1]:2376", getenv: noEnv}, "fd00::1"},
		{"a daemon bound to every interface is this machine",
			hostResolver{dockerHost: "tcp://0.0.0.0:2375", getenv: noEnv}, "127.0.0.1"},
		{"a local daemon, from the host",
			hostResolver{dockerHost: socket, getenv: noEnv, detect: onAHost}, "127.0.0.1"},
		{"a local daemon, from a container on the host's network",
			hostResolver{dockerHost: socket, getenv: noEnv, detect: inAContainer, bridge: bridgeVisible, gateway: gatewayAt("192.168.2.1")}, "127.0.0.1"},
		{"a local daemon, from a container with its own network",
			hostResolver{dockerHost: socket, getenv: noEnv, detect: inAContainer, bridge: noBridge, gateway: gatewayAt("172.17.0.1")}, "172.17.0.1"},
		{"a daemon address with no scheme, as a test double has",
			hostResolver{dockerHost: "mock", getenv: noEnv, detect: onAHost}, "127.0.0.1"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := tc.resolver.resolve()

			require.NoError(t, err, "the address can be worked out")
			assert.Equal(t, tc.want, got.Host, "the address to dial")
			assert.NotEmpty(t, got.Source, "and what chose it, for the error a failed start returns")
		})
	}
}

func TestAContainerWithNoGatewayCannotWorkOutTheAddress(t *testing.T) {
	resolver := hostResolver{dockerHost: "unix:///var/run/docker.sock", getenv: noEnv, detect: inAContainer, bridge: noBridge, gateway: noGateway}

	_, err := resolver.resolve()

	require.ErrorIs(t, err, ErrUnresolvedHost, "there is nothing to guess from")
	var unresolved *UnresolvedHostError
	require.ErrorAs(t, err, &unresolved, "with the details")
	assert.Equal(t, "/.dockerenv exists", unresolved.Evidence, "including why this process was taken to be in a container")
}

func TestThePortIsPublishedWhereItWillBeDialled(t *testing.T) {
	cases := []struct {
		resolved string
		bind     string
		dial     string
	}{
		{"127.0.0.1", "127.0.0.1", "127.0.0.1"},
		{"localhost", "127.0.0.1", "localhost"},
		{"::1", "127.0.0.1", "127.0.0.1"},
		{"127.0.1.1", "127.0.0.1", "127.0.0.1"},
		{"172.17.0.1", "0.0.0.0", "172.17.0.1"},
		{"build-host", "0.0.0.0", "build-host"},
	}
	for _, tc := range cases {
		t.Run(tc.resolved, func(t *testing.T) {
			assert.Equal(t, tc.bind, bindIP(tc.resolved), "loopback stays off the network; anything else needs every interface")
			assert.Equal(t, tc.dial, dialHost(tc.resolved), "and a loopback publish only answers at 127.0.0.1")
		})
	}
}

func TestContainerisationIsReadFromTheMarkerFiles(t *testing.T) {
	only := func(path string) func(string) bool {
		return func(candidate string) bool { return candidate == path }
	}

	assert.True(t, containerisationFrom(only("/.dockerenv")).containerised, "Docker creates /.dockerenv in every container")
	assert.True(t, containerisationFrom(only("/run/.containerenv")).containerised, "Podman creates /run/.containerenv")
	assert.False(t, containerisationFrom(only("/etc/hosts")).containerised, "a host has neither")
}

// The routing tables are as /proc/net/route prints them.
const (
	hostRoutes = "Iface\tDestination\tGateway \tFlags\n" +
		"eth0\t00000000\t0102A8C0\t0003\n" +
		"docker0\t000011AC\t00000000\t0001\n"
	containerRoutes = "Iface\tDestination\tGateway \tFlags\n" +
		"eth0\t00000000\t010011AC\t0003\n" +
		"eth0\t000011AC\t00000000\t0001\n"
)

func fileHolding(content string) func(string) ([]byte, error) {
	return func(string) ([]byte, error) { return []byte(content), nil }
}

func unreadable(string) ([]byte, error) { return nil, os.ErrNotExist }

func TestARuntimeBridgeMeansTheHostsNetwork(t *testing.T) {
	interfaces := func(names ...string) func(string) ([]string, error) {
		return func(string) ([]string, error) { return names, nil }
	}
	noSys := func(string) ([]string, error) { return nil, errors.New("no /sys") }

	_, onHost := runtimeBridge(fileHolding(hostRoutes), noSys)
	assert.True(t, onHost, "docker0 in the routing table is the daemon host's network")

	_, inContainer := runtimeBridge(fileHolding(containerRoutes), interfaces("docker0"))
	assert.False(t, inContainer, "a container sees only eth0; /sys is not consulted when the routing table can be read, because it may be the host's")

	_, fromSys := runtimeBridge(unreadable, interfaces("lo", "br-3f1a9c4b7e2d"))
	assert.True(t, fromSys, "without a routing table, /sys is the fallback")
}

func TestTheDefaultGatewayIsReadFromTheRoutingTable(t *testing.T) {
	gateway, err := parseDefaultGateway([]byte(containerRoutes))
	require.NoError(t, err, "the table has a default route")
	assert.Equal(t, "172.17.0.1", gateway, "addresses in the table are little-endian hex")

	_, err = parseDefaultGateway([]byte("Iface\tDestination\tGateway\neth0\t000011AC\t00000000\n"))
	assert.ErrorIs(t, err, errNoDefaultRoute, "a table with no default route has no gateway")
}

// The routing table comes from the kernel, but a parser must not panic on
// anything.
func FuzzParseDefaultGateway(f *testing.F) {
	f.Add([]byte(hostRoutes))
	f.Add([]byte(containerRoutes))
	f.Add([]byte("eth0\t00000000\tZZZZZZZZ\n"))
	f.Fuzz(func(t *testing.T, routes []byte) {
		gateway, err := parseDefaultGateway(routes)
		if err == nil {
			assert.NotEmpty(t, gateway, "a gateway that is found is an address")
		}
	})
}
