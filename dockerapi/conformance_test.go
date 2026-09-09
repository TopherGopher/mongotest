package dockerapi_test

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/tophergopher/mongotest/dockerapi"
	"github.com/tophergopher/mongotest/dockerclient"
	"github.com/tophergopher/mongotest/dockerclient/dockerclienttest"
	"github.com/tophergopher/mongotest/dockermock"
)

// newConformanceClient points a real dockerapi client at a fake daemon whose
// routes are backed by an in-memory store, so the suite exercises the whole
// HTTP path while still asserting on state that actually changes.
func newConformanceClient(tb testing.TB) dockerclient.Client {
	tb.Helper()
	d := dockermock.NewDaemon()
	tb.Cleanup(d.Close)
	d.ServeFake(dockermock.NewFake())
	c, err := dockerapi.New(dockerapi.WithHost(d.Host()))
	require.NoError(tb, err, "a client against the fake daemon must construct")
	return c
}

// TestConformance holds this client to the behaviour every implementation
// shares, so swapping it for mobyclient or a double changes nothing a caller
// can observe.
func TestConformance(t *testing.T) {
	dockerclienttest.Conformance(t, func(t *testing.T) dockerclient.Client {
		return newConformanceClient(t)
	})
}

// BenchmarkClient measures the calls in the interface on the same scale as
// every other implementation. The detailed, dockerapi-specific benchmarks
// live in benchmark_test.go.
func BenchmarkClient(b *testing.B) {
	dockerclienttest.Benchmarks(b, func(b *testing.B) dockerclient.Client {
		return newConformanceClient(b)
	})
}
