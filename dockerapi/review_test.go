package dockerapi

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tophergopher/mongotest/dockerclient"
)

// tarBroke stands in for a failure raised while the request body was being
// produced, which is what a tar writer error looks like to http.Client.Do.
var tarBroke = errors.New("archive/tar: write too long")

func TestTransportFailureIsNotReportedAsAConnectionProblem(t *testing.T) {
	// http.Client.Do wraps everything it returns in *url.Error, and
	// *url.Error satisfies net.Error, so matching on the net.Error interface
	// classifies every transport failure as "the daemon is unreachable".
	// A tar error raised while streaming an archive is the case that matters:
	// the daemon was reachable, and telling the caller to start docker sends
	// them nowhere.
	c := newMockClient(t, func(*http.Request) (*http.Response, error) { return nil, tarBroke })

	_, err := c.ContainerInspect(context.Background(), "c0ffee1234ab")
	require.Error(t, err, "a failing round trip must produce an error")
	assert.NotErrorIs(t, err, dockerclient.ErrConnectionFailed,
		"a failure that is not a connection problem must not claim the daemon is unreachable")
	assert.ErrorIs(t, err, tarBroke, "the underlying cause must stay in the chain")
	assert.NotContains(t, err.Error(), "Is the docker daemon running",
		"the message must not advise starting a daemon that answered")
}

func TestRefusedConnectionIsStillAConnectionProblem(t *testing.T) {
	// The counterpart to the test above: narrowing the match must not stop
	// real connection failures from being classified.
	refused := &net.OpError{Op: "dial", Net: "unix", Err: errors.New("connect: connection refused")}
	c := newMockClient(t, func(*http.Request) (*http.Response, error) { return nil, refused })

	_, err := c.ContainerInspect(context.Background(), "c0ffee1234ab")
	require.Error(t, err, "a refused dial must produce an error")
	assert.ErrorIs(t, err, dockerclient.ErrConnectionFailed, "a refused dial is a connection problem")
	assert.Contains(t, err.Error(), "Is the docker daemon running", "the fix must be stated")
}

func TestCancelledContextIsReportedAsCancellation(t *testing.T) {
	// WrapConnectionError deliberately passes context errors through, which
	// means they fall to the ResponseError branch and are described as the
	// daemon speaking the wrong API version. The daemon never answered.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	c := newMockClient(t, func(req *http.Request) (*http.Response, error) {
		return nil, req.Context().Err()
	})

	_, err := c.ContainerInspect(ctx, "c0ffee1234ab")
	require.Error(t, err, "a cancelled context must produce an error")
	assert.ErrorIs(t, err, context.Canceled, "the caller must be able to see their own cancellation")
	assert.NotErrorIs(t, err, dockerclient.ErrDaemonResponse,
		"a cancellation is not a bad response; the daemon never responded")
	assert.NotContains(t, err.Error(), "DOCKER_API_VERSION",
		"a cancellation must not blame the negotiated API version")
}

func TestStatusErrorTruncatesOnARuneBoundary(t *testing.T) {
	// Cutting a UTF-8 string at a byte offset can leave half a rune, which
	// then propagates into logs and into anything that re-encodes the error.
	long := strings.Repeat("日", 300) // three bytes per rune, so byte 200 falls mid-rune
	resp := &http.Response{
		StatusCode: http.StatusInternalServerError,
		Body:       io.NopCloser(strings.NewReader(`{"message":"` + long + `"}`)),
	}
	err := newStatusError(resp, http.MethodGet, "/containers/x/json")
	assert.True(t, utf8.ValidString(err.Message), "a truncated daemon message must still be valid UTF-8")
	assert.True(t, utf8.ValidString(err.Error()), "the rendered error must be valid UTF-8")
	assert.Contains(t, err.Message, "…", "a truncated message must show that it was cut")
}

func TestParseHostRejectsAUnixAuthority(t *testing.T) {
	// unix://remote-box/var/run/docker.sock parses with Host "remote-box" and
	// Path "/var/run/docker.sock". Taking the path and dropping the host
	// connects to the local socket instead of the one the user named, which
	// succeeds quietly and is the worst available failure.
	_, err := parseHost("unix://remote-box/var/run/docker.sock")
	require.Error(t, err, "a unix host with an authority must be refused")
	assert.ErrorIs(t, err, dockerclient.ErrInvalidArgument, "it is a configuration mistake")
	assert.Contains(t, err.Error(), "remote-box", "the error must name the part that cannot be honoured")

	ep, err := parseHost("unix:///var/run/docker.sock")
	require.NoError(t, err, "an ordinary unix socket must still parse")
	assert.Equal(t, "/var/run/docker.sock", ep.addr, "the socket path must be taken from the URL path")
}

func TestParseHostRejectsARelativeUnixPath(t *testing.T) {
	// The comment said docker does not accept this form, while the code
	// accepted it. The daemon requires an absolute socket path.
	_, err := parseHost("unix://relative.sock")
	require.Error(t, err, "a relative socket path must be refused")
	assert.ErrorIs(t, err, dockerclient.ErrInvalidArgument, "it is a configuration mistake")
	assert.Contains(t, err.Error(), "absolute", "the error must say the path has to be absolute")
	assert.Contains(t, err.Error(), "relative.sock", "the error must quote what was given")
}

func TestRuntimeIsDetectedEvenWhenTheAPIVersionIsPinned(t *testing.T) {
	// Pinning skips negotiation, so GET /version is never issued. Podman
	// users are the most likely to pin, precisely because Podman capped at
	// 1.41, and they are the ones who need the product name in errors.
	// Podman sets Libpod-API-Version on every response, so identification
	// does not need a dedicated round trip.
	c := newMockClient(t, func(req *http.Request) (*http.Response, error) {
		resp, err := mockJSON(http.StatusOK, ContainerInspect{ID: "c0ffee1234ab"})(req)
		if resp != nil {
			resp.Header.Set("Libpod-API-Version", "5.7.1")
		}
		return resp, err
	}, WithAPIVersion("1.41"))

	_, err := c.ContainerInspect(context.Background(), "c0ffee1234ab")
	require.NoError(t, err, "a pinned client must still work")
	assert.Equal(t, "1.41", c.APIVersion(), "the pin must be honoured")
	assert.Equal(t, RuntimePodman, c.Runtime(), "the response header identifies the daemon without a /version call")
	assert.Contains(t, c.ServerProduct(), "5.7.1", "the product must name the version from the header")
}

func TestTransportPoolsConnectionsPerHost(t *testing.T) {
	// Every request from one client goes to one host, so MaxIdleConnsPerHost
	// is the limit that binds. Its default is 2, which throws away
	// connections under the concurrency this package is built for.
	tr := newTransport(endpoint{scheme: "unix", addr: "/var/run/docker.sock"}, nil)
	assert.Equal(t, tr.MaxIdleConns, tr.MaxIdleConnsPerHost,
		"the per-host idle limit must match the global one, or the global one has no effect")
	assert.Positive(t, tr.IdleConnTimeout, "idle connections must not be held forever")
}
