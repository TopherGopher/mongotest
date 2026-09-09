package dockerapi

import (
	"context"
	"net"
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tophergopher/mongotest/dockermock"
)

func TestContainerCreateRejectsBadInputBeforeRequest(t *testing.T) {
	c := newMockClient(t, noRequest(t))
	_, _, err := c.ContainerCreate(context.Background(), "", ContainerConfig{})
	assert.ErrorIs(t, err, ErrInvalidArgument, "an empty image must be rejected client side")
	_, _, err = c.ContainerCreate(context.Background(), "bad name!", ContainerConfig{Image: "mongo:8"})
	assert.ErrorIs(t, err, ErrInvalidArgument, "an invalid container name must be rejected client side")
	_, _, err = c.ContainerCreate(context.Background(), "x", ContainerConfig{Image: "mongo:8"})
	assert.ErrorIs(t, err, ErrInvalidArgument, "a one-character name must be rejected; the daemon requires at least two")
}

func TestIDGuardsOnEveryContainerEndpoint(t *testing.T) {
	c := newMockClient(t, noRequest(t))
	ctx := context.Background()
	calls := map[string]func() error{
		"start":   func() error { return c.ContainerStart(ctx, " ") },
		"remove":  func() error { return c.ContainerRemove(ctx, "a/b", RemoveOptions{}) },
		"inspect": func() error { _, err := c.ContainerInspect(ctx, ""); return err },
		"top":     func() error { _, err := c.ContainerTop(ctx, "?"); return err },
		"copy":    func() error { return c.CopyToContainer(ctx, "", "/tmp", []File{{Name: "x"}}) },
		"exec create": func() error {
			_, err := c.ExecCreate(ctx, "../x", ExecConfig{Cmd: []string{"true"}})
			return err
		},
		"exec start":    func() error { _, _, err := c.ExecStart(ctx, ""); return err },
		"exec inspect":  func() error { _, err := c.ExecInspect(ctx, "a b"); return err },
		"exec":          func() error { _, err := c.Exec(ctx, "", "true"); return err },
		"image inspect": func() error { _, err := c.ImageInspect(ctx, "mongo:8?x=1"); return err },
		"image pull":    func() error { return c.ImagePull(ctx, "Mongo") },
	}
	for name, call := range calls {
		assert.ErrorIs(t, call(), ErrInvalidArgument, "%s: an invalid id or reference must be rejected before any request", name)
	}
}

func TestImageInspectAcceptsImageIDs(t *testing.T) {
	c := newMockClient(t, func(req *http.Request) (*http.Response, error) {
		return mockJSON(200, ImageInspect{ID: "x"})(req)
	})
	for _, id := range []string{"sha256:" + strings.Repeat("ab", 32), "41c3b7abb48e"} {
		_, err := c.ImageInspect(context.Background(), id)
		assert.NoError(t, err, "image id %q must be accepted by inspect", id)
	}
}

func TestExecCreateGuards(t *testing.T) {
	c := newMockClient(t, mockJSON(http.StatusCreated, execCreateResponse{ID: "e"}))
	ctx := context.Background()
	_, err := c.ExecCreate(ctx, "abc", ExecConfig{})
	assert.Same(t, ErrNoCommand, err, "an empty Cmd returns the predeclared ErrNoCommand")
	_, err = c.ExecCreate(ctx, "abc", ExecConfig{Cmd: []string{""}})
	assert.Same(t, ErrNoCommand, err, "an empty program name returns ErrNoCommand")
	_, err = c.ExecCreate(ctx, "abc", ExecConfig{Cmd: []string{"sh"}, Env: []string{"NOEQUALS"}})
	assert.ErrorIs(t, err, ErrInvalidArgument, "an env entry without '=' is rejected")
	_, err = c.ExecCreate(ctx, "abc", ExecConfig{Cmd: []string{"sh"}, WorkingDir: "relative"})
	assert.ErrorIs(t, err, ErrInvalidArgument, "a relative working directory is rejected")
	_, err = c.ExecCreate(ctx, "abc", ExecConfig{Cmd: []string{"sh"}, Env: []string{"A=1"}, WorkingDir: "/tmp"})
	assert.NoError(t, err, "a complete, valid exec config is accepted")
}

func TestExecCreateVersionGates(t *testing.T) {
	c := newMockClient(t, mockJSON(http.StatusCreated, execCreateResponse{ID: "e"}), WithAPIVersion("1.30"))
	ctx := context.Background()
	_, err := c.ExecCreate(ctx, "abc", ExecConfig{Cmd: []string{"sh"}, Env: []string{"A=1"}})
	require.NoError(t, err, "Env needs API 1.25, which 1.30 satisfies")
	_, err = c.ExecCreate(ctx, "abc", ExecConfig{Cmd: []string{"sh"}, WorkingDir: "/tmp"})
	require.ErrorIs(t, err, ErrAPIVersion, "WorkingDir needs API 1.35 and must be refused on 1.30")
	assert.Contains(t, err.Error(), "1.35", "the required version is named")
	assert.Contains(t, err.Error(), "1.30", "the negotiated version is named")
}

func TestCopyToContainerDestMustBeAbsolute(t *testing.T) {
	c := newMockClient(t, noRequest(t))
	for _, dest := range []string{"", "tmp", "./tmp", "../etc", "/etc/../root"} {
		err := c.CopyToContainer(context.Background(), "abc", dest, []File{{Name: "x"}})
		assert.ErrorIs(t, err, ErrInvalidArgument, "destination %q must be rejected before any request", dest)
	}
}

func TestConnectionErrorsAreActionable(t *testing.T) {
	c, err := New(WithHost("unix:///nonexistent/dir/docker.sock"))
	require.NoError(t, err, "construction does not dial")
	err = c.Negotiate(context.Background())
	require.ErrorIs(t, err, ErrConnectionFailed, "a missing socket is a connection failure")
	assert.Contains(t, err.Error(), "/nonexistent/dir/docker.sock", "the error names the socket it tried")
	assert.Contains(t, err.Error(), "Start the docker daemon", "the error says what to do")
	assert.NotContains(t, err.Error(), "api.moby.localhost", "the placeholder host must not leak into messages")

	fd := newDaemon(t, dockermock.OverTCP())
	c2, err := New(WithHost(fd.Host()))
	require.NoError(t, err, "construction against the fake")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	assert.ErrorIs(t, c2.Negotiate(ctx), context.Canceled, "context errors pass through undecorated so callers can compare them")

	l, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err, "reserving a port that nothing will listen on")
	addr := l.Addr().String()
	l.Close()
	c3, err := New(WithHost("tcp://" + addr))
	require.NoError(t, err, "construction against a closed port")
	err = c3.Negotiate(context.Background())
	require.ErrorIs(t, err, ErrConnectionFailed, "connection refused is a connection failure")
	assert.Contains(t, err.Error(), addr, "the error names the address")
	assert.Contains(t, err.Error(), "Is the docker daemon running?", "the error asks the obvious question")
}
