package dockerapi

import (
	"context"
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tophergopher/mongotest/internal/fakedaemon"
)

func TestCompareVersions(t *testing.T) {
	tests := []struct {
		a, b string
		want int
	}{
		{"1.44", "1.44", 0},
		{"1.44", "1.9", 1}, // numeric, not lexical
		{"1.9", "1.44", -1},
		{"1.41", "1.44", -1},
		{"2.0", "1.99", 1},
		{"1.44", "", 1}, // empty sorts lowest
	}
	for _, tc := range tests {
		assert.Equal(t, tc.want, compareVersions(tc.a, tc.b), "compareVersions(%q, %q) must compare major.minor numerically", tc.a, tc.b)
	}
}

// versionServer returns a fake daemon whose /version reports the given
// window and a ping route to exercise versioned paths.
func versionServer(t *testing.T, apiVersion, minVersion string) *fakedaemon.Server {
	fd := fakedaemon.New(t)
	fd.ServeVersion(apiVersion, minVersion)
	fd.Handle("GET", "/_ping", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(200) })
	return fd
}

func ping(t *testing.T, c *Client) {
	t.Helper()
	resp, err := c.do(context.Background(), http.MethodGet, "/_ping", nil, nil)
	require.NoError(t, err, "ping through the client must succeed")
	resp.Body.Close()
}

func versionCalls(fd *fakedaemon.Server) int {
	n := 0
	for _, r := range fd.Requests() {
		if r.RawPath == "/version" {
			n++
		}
	}
	return n
}

func TestNegotiateChoosesPreferredWhenServerIsNewer(t *testing.T) {
	fd := versionServer(t, "1.54", "1.40")
	c, err := New(WithHost(fd.Host()))
	require.NoError(t, err, "client construction")
	assert.Empty(t, c.APIVersion(), "before the first request no version is known")
	ping(t, c)
	assert.Equal(t, PreferredAPIVersion, c.APIVersion(), "a newer daemon must be talked to at our preferred version")
	reqs := fd.Requests()
	require.Len(t, reqs, 2, "negotiation then ping")
	assert.Equal(t, "/version", reqs[0].RawPath, "negotiation is unversioned")
	assert.Equal(t, "/v"+PreferredAPIVersion+"/_ping", reqs[1].RawPath, "subsequent requests carry the chosen version")
}

func TestNegotiateFallsBackToServerMax(t *testing.T) {
	fd := versionServer(t, "1.41", "1.24") // podman-like
	c, err := New(WithHost(fd.Host()))
	require.NoError(t, err, "client construction")
	ping(t, c)
	assert.Equal(t, "1.41", c.APIVersion(), "an older daemon lowers the version to its maximum")
	assert.Equal(t, "/v1.41/_ping", fd.Requests()[1].RawPath, "the lowered version must prefix requests")
}

func TestNegotiateToleratesMissingMinVersion(t *testing.T) {
	fd := versionServer(t, "1.40", "")
	c, err := New(WithHost(fd.Host()))
	require.NoError(t, err, "client construction")
	ping(t, c)
	assert.Equal(t, "1.40", c.APIVersion(), "a daemon that omits MinAPIVersion must still negotiate")
}

func TestNegotiateServerMinimumTooHigh(t *testing.T) {
	fd := versionServer(t, "1.70", "1.60")
	c, err := New(WithHost(fd.Host()))
	require.NoError(t, err, "client construction")
	_, err = c.do(context.Background(), http.MethodGet, "/_ping", nil, nil)
	require.ErrorIs(t, err, ErrAPIVersion, "a daemon window above ours is an API version problem")
	assert.Contains(t, err.Error(), "1.60", "the error must state the daemon's minimum")
	assert.Contains(t, err.Error(), PreferredAPIVersion, "the error must state our maximum")
	assert.Contains(t, err.Error(), "Upgrade mongotest", "the error must say what to do")
	assert.Len(t, fd.Requests(), 1, "only the /version request may be sent when negotiation fails")
}

func TestNegotiateServerTooOld(t *testing.T) {
	fd := versionServer(t, "1.20", "")
	c, err := New(WithHost(fd.Host()))
	require.NoError(t, err, "client construction")
	err = c.Negotiate(context.Background())
	require.ErrorIs(t, err, ErrAPIVersion, "a daemon below our minimum is an API version problem")
	assert.Contains(t, err.Error(), "1.20", "the error must state the daemon's maximum")
	assert.Contains(t, err.Error(), MinSupportedAPIVersion, "the error must state our minimum")
	assert.Contains(t, err.Error(), "Upgrade the docker daemon", "the error must say what to do")
}

func TestNegotiateHappensOnce(t *testing.T) {
	fd := versionServer(t, "1.54", "1.40")
	c, err := New(WithHost(fd.Host()))
	require.NoError(t, err, "client construction")
	for i := 0; i < 5; i++ {
		ping(t, c)
	}
	require.NoError(t, c.Negotiate(context.Background()), "an explicit Negotiate after success is a no-op")
	assert.Equal(t, 1, versionCalls(fd), "/version must be called exactly once per client")
}

func TestNegotiateRetriesAfterFailure(t *testing.T) {
	fd := fakedaemon.New(t)
	calls := 0
	fd.Handle("GET", "/version", func(w http.ResponseWriter, _ *http.Request) {
		calls++
		if calls == 1 {
			fakedaemon.Error(w, 500, "boom")
			return
		}
		fakedaemon.JSON(w, 200, versionResponse{APIVersion: "1.44"})
	})
	c, err := New(WithHost(fd.Host()))
	require.NoError(t, err, "client construction")
	err = c.Negotiate(context.Background())
	require.Error(t, err, "the first negotiation must fail when the daemon answers 500")
	assert.Contains(t, err.Error(), "boom", "the daemon's message must be preserved")
	require.NoError(t, c.Negotiate(context.Background()), "negotiation must be retried after a failure, not cached")
}

func TestPinnedVersionSkipsNegotiation(t *testing.T) {
	fd := versionServer(t, "1.54", "1.40")
	t.Run("WithAPIVersion", func(t *testing.T) {
		c, err := New(WithHost(fd.Host()), WithAPIVersion("1.43"))
		require.NoError(t, err, "a well-formed pin is accepted")
		fd.Reset()
		ping(t, c)
		reqs := fd.Requests()
		require.Len(t, reqs, 1, "a pinned client must not call /version")
		assert.Equal(t, "/v1.43/_ping", reqs[0].RawPath, "the pinned version must prefix requests")
	})
	t.Run("DOCKER_API_VERSION", func(t *testing.T) {
		t.Setenv("DOCKER_API_VERSION", "1.42")
		c, err := New(WithHost(fd.Host()))
		require.NoError(t, err, "DOCKER_API_VERSION is accepted")
		fd.Reset()
		ping(t, c)
		reqs := fd.Requests()
		require.Len(t, reqs, 1, "DOCKER_API_VERSION must skip /version")
		assert.Equal(t, "/v1.42/_ping", reqs[0].RawPath, "the environment pin must prefix requests")
		assert.Equal(t, "1.42", c.APIVersion(), "APIVersion reports the pin")
	})
	t.Run("malformed pin is rejected", func(t *testing.T) {
		_, err := New(WithHost(fd.Host()), WithAPIVersion("latest"))
		require.ErrorIs(t, err, ErrInvalidArgument, "a non major.minor pin is an invalid argument")
		assert.Contains(t, err.Error(), "1.44", "the error must show the expected form")
	})
}
