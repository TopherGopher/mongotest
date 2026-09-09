package dockerapi

import (
	"context"
	"errors"
	"net"
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tophergopher/mongotest/internal/fakedaemon"
)

func TestParseImageRef(t *testing.T) {
	good := []struct{ ref, name, tag, digest string }{
		{"mongo", "mongo", "latest", ""},
		{"mongo:8", "mongo", "8", ""},
		{"mongo:8.0-noble", "mongo", "8.0-noble", ""},
		{"library/mongo:8", "library/mongo", "8", ""},
		{"docker.io/library/mongo:8", "docker.io/library/mongo", "8", ""},
		{"localhost:5000/mongo", "localhost:5000/mongo", "latest", ""},
		{"localhost:5000/team/mongo:8.0", "localhost:5000/team/mongo", "8.0", ""},
		{"GHCR.io/org-name/img:v1", "GHCR.io/org-name/img", "v1", ""}, // uppercase allowed in the registry host only
		{"mongo:8/json", "mongo:8/json", "latest", ""},                // legal: host "mongo:8", repository "json"
		{"[::1]:5000/mongo", "[::1]:5000/mongo", "latest", ""},
		{"mongo@sha256:" + strings.Repeat("a", 64), "mongo", "", "sha256:" + strings.Repeat("a", 64)},
		{"mongo:8@sha256:" + strings.Repeat("0", 64), "mongo", "8", "sha256:" + strings.Repeat("0", 64)},
		{"my__image.name-x", "my__image.name-x", "latest", ""},
	}
	for _, tc := range good {
		r, err := parseImageRef(tc.ref)
		require.NoError(t, err, "%q is a valid reference", tc.ref)
		assert.Equal(t, tc.name, r.name, "%q: repository part", tc.ref)
		assert.Equal(t, tc.tag, r.tag, "%q: tag (latest when absent and no digest)", tc.ref)
		assert.Equal(t, tc.digest, r.digest, "%q: digest", tc.ref)
	}
	bad := []string{
		"", " ", "Mongo", "mongo:8:9", "mongo:", ":8", "mongo/", "/mongo", "mongo//x", "mongo:8 ", "mongo:-8",
		"mongo@sha256:short", "mongo@notadigest", "mon go", "mongo:" + strings.Repeat("t", 129),
		strings.Repeat("a", 256), "mongo?x=1", "mongo#1", "ghcr.io/Org-Name/img:v1",
	}
	for _, ref := range bad {
		_, err := parseImageRef(ref)
		require.ErrorIs(t, err, ErrInvalidArgument, "%q must be rejected as an invalid argument", ref)
		assert.Contains(t, err.Error(), "repository", "%q: the error must explain the reference grammar", ref)
	}
	_, err := parseImageRef("Mongo:8")
	assert.Contains(t, err.Error(), "lowercase", "an uppercase repository gets the daemon's familiar wording")
}

func TestCheckID(t *testing.T) {
	for _, in := range []string{"abc123", "  abc123 ", "/mongotest-1", "mongotest_1.x-y", "de686f1e8de9bcabdc4b20b2d9b80d33f90ed79aa7588ad907d953ad63dc9d3c"} {
		got, err := checkID("container", in)
		require.NoError(t, err, "%q is an acceptable id", in)
		assert.NotEmpty(t, got, "%q: a cleaned id is returned", in)
		assert.False(t, strings.ContainsAny(got, " /"), "%q: whitespace and the leading slash are stripped, got %q", in, got)
	}
	for _, in := range []string{"", "   ", "a/b", "../x", "abc?force=1", "abc#", "-abc", ".abc", "a b", "a\nb"} {
		_, err := checkID("container", in)
		require.ErrorIs(t, err, ErrInvalidArgument, "%q must be rejected so it cannot alter the URL path", in)
	}
	_, err := checkID("exec", "")
	assert.Contains(t, err.Error(), "exec id", "the error must name the kind of id that was empty")
	assert.Contains(t, err.Error(), "ExecCreate", "the error must say where a valid id comes from")
}

func TestValidateContainerConfig(t *testing.T) {
	ok := ContainerConfig{
		Image:        "mongo:8",
		Labels:       map[string]string{"mongotest": "regression"},
		ExposedPorts: map[string]struct{}{"27017/tcp": {}, "27018": {}},
		HostConfig: &HostConfig{PortBindings: map[string][]PortBinding{
			"27017/tcp": {{HostIP: "127.0.0.1", HostPort: "34819"}, {HostIP: "", HostPort: ""}, {HostPort: "40000-40010"}},
			"53/udp":    {{HostIP: "::1", HostPort: "5353"}},
		}},
	}
	require.NoError(t, ok.validate(), "a config with valid ports, bindings and labels must pass")
	bad := map[string]ContainerConfig{
		"no image":          {},
		"bad image":         {Image: "Mongo"},
		"empty label key":   {Image: "mongo", Labels: map[string]string{"": "x"}},
		"exposed no number": {Image: "mongo", ExposedPorts: map[string]struct{}{"tcp": {}}},
		"exposed port zero": {Image: "mongo", ExposedPorts: map[string]struct{}{"0/tcp": {}}},
		"exposed too big":   {Image: "mongo", ExposedPorts: map[string]struct{}{"70000/tcp": {}}},
		"bad proto":         {Image: "mongo", ExposedPorts: map[string]struct{}{"27017/icmp": {}}},
		"binding key":       {Image: "mongo", HostConfig: &HostConfig{PortBindings: map[string][]PortBinding{"x/tcp": {{}}}}},
		"binding host ip":   {Image: "mongo", HostConfig: &HostConfig{PortBindings: map[string][]PortBinding{"27017/tcp": {{HostIP: "localhost"}}}}},
		"binding host port": {Image: "mongo", HostConfig: &HostConfig{PortBindings: map[string][]PortBinding{"27017/tcp": {{HostPort: "abc"}}}}},
		"binding range":     {Image: "mongo", HostConfig: &HostConfig{PortBindings: map[string][]PortBinding{"27017/tcp": {{HostPort: "40010-40000"}}}}},
		"empty cmd element": {Image: "mongo", Cmd: []string{"--replSet", ""}},
	}
	for name, cfg := range bad {
		err := cfg.validate()
		require.ErrorIs(t, err, ErrInvalidArgument, "%s: must be rejected as an invalid argument", name)
		var ia *InvalidArgumentError
		require.True(t, errors.As(err, &ia), "%s: the typed error must be extractable", name)
		assert.NotEmpty(t, ia.Fix, "%s: every validation error must say what to do", name)
	}
}

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

	fd := fakedaemon.NewTCP(t)
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
