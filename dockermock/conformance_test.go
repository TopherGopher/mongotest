package dockermock_test

import (
	"testing"

	"github.com/tophergopher/mongotest/dockerclient"
	"github.com/tophergopher/mongotest/dockerclient/dockerclienttest"
	"github.com/tophergopher/mongotest/dockermock"
)

// TestFakeConformance holds the Fake to the same behaviour as a real client.
// A double that behaves differently from the thing it stands in for is worse
// than no double at all, so this is the test that keeps it honest.
func TestFakeConformance(t *testing.T) {
	dockerclienttest.Conformance(t, func(t *testing.T) dockerclient.Client {
		return dockermock.NewFake()
	})
}

// BenchmarkFake measures the Fake on the same scale as the real clients,
// which is the floor: whatever a client costs above this is its own work
// rather than the interface's.
func BenchmarkFake(b *testing.B) {
	dockerclienttest.Benchmarks(b, func(b *testing.B) dockerclient.Client {
		return dockermock.NewFake()
	})
}
