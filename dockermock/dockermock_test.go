package dockermock_test

import (
	"bytes"
	"context"
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tophergopher/mongotest/dockerapi"
	"github.com/tophergopher/mongotest/dockerclient"
	"github.com/tophergopher/mongotest/dockermock"
)

func TestMockRecordsEveryCall(t *testing.T) {
	m := &dockermock.Mock{}
	ctx := context.Background()

	_ = m.Host()
	_ = m.ImagePull(ctx, "mongo:8")
	_, _ = m.ImageInspect(ctx, "mongo:8")
	_, _, _ = m.ContainerCreate(ctx, "n", dockerclient.ContainerConfig{Image: "mongo:8"})
	_ = m.ContainerStart(ctx, "c1")
	_, _ = m.ContainerInspect(ctx, "c1")
	_, _ = m.ContainerTop(ctx, "c1")
	_ = m.CopyToContainer(ctx, "c1", "/tmp", nil)
	_ = m.CopyArchiveToContainer(ctx, "c1", "/tmp", bytes.NewReader(nil))
	_, _ = m.ExecCreate(ctx, "c1", dockerclient.ExecConfig{Cmd: []string{"true"}})
	_ = m.ExecStartTo(ctx, "e1", nil, nil)
	_, _ = m.ExecInspect(ctx, "e1")
	_, _ = m.Exec(ctx, "c1", "true")
	_ = m.ContainerRemove(ctx, "c1", dockerclient.RemoveOptions{})

	want := []string{
		"Host", "ImagePull", "ImageInspect", "ContainerCreate", "ContainerStart",
		"ContainerInspect", "ContainerTop", "CopyToContainer", "CopyArchiveToContainer",
		"ExecCreate", "ExecStartTo", "ExecInspect", "Exec", "ContainerRemove",
	}
	assert.Equal(t, want, m.Methods(), "every method must record its call, in the order they were made")
}

func TestMockUnsetFunctionsSucceed(t *testing.T) {
	m := &dockermock.Mock{}
	ctx := context.Background()
	// A test that only cares about one call should not have to script the
	// rest, so an unset field succeeds with a zero result.
	require.NoError(t, m.ContainerStart(ctx, "c1"), "an unset function must succeed")
	id, warnings, err := m.ContainerCreate(ctx, "", dockerclient.ContainerConfig{})
	require.NoError(t, err, "an unset create must succeed")
	assert.Empty(t, id, "an unset create returns the zero id")
	assert.Empty(t, warnings, "an unset create returns no warnings")
	assert.Equal(t, "mock://dockermock", m.Host(), "Host reports a recognisable placeholder")
}

func TestMockArgumentsAndReset(t *testing.T) {
	m := &dockermock.Mock{}
	ctx := context.Background()
	cfg := dockerclient.ContainerConfig{Image: "mongo:8"}
	_, _, _ = m.ContainerCreate(ctx, "mongotest-1", cfg)

	calls := m.CallsTo("ContainerCreate")
	require.Len(t, calls, 1, "one create was made")
	require.Len(t, calls[0].Args, 2, "create records its name and config, the context excluded")
	assert.Equal(t, "mongotest-1", calls[0].Args[0], "the name is recorded so a test can assert on it")
	assert.Equal(t, cfg, calls[0].Args[1], "the config is recorded as passed")

	m.Reset()
	assert.Empty(t, m.Calls(), "Reset forgets the recorded calls")
	require.NoError(t, m.ContainerStart(ctx, "c1"), "the mock still works after a reset")
	assert.Len(t, m.Calls(), 1, "recording resumes after a reset")
}

func TestMockFunctionsAreCalled(t *testing.T) {
	sentinel := &dockerclient.StatusError{StatusCode: 500, Message: "boom", Method: "POST", Path: "/x"}
	m := &dockermock.Mock{
		ContainerStartFunc: func(ctx context.Context, id string) error { return sentinel },
	}
	err := m.ContainerStart(context.Background(), "c1")
	assert.Same(t, sentinel, err, "a set function decides the result")
}

func TestFakeRejectsSameInputAsARealClient(t *testing.T) {
	f := dockermock.NewFake()
	ctx := context.Background()
	// A double that accepts what a daemon refuses hides bugs until
	// production, so the Fake runs the same validation.
	err := f.ImagePull(ctx, "Mongo:8")
	assert.ErrorIs(t, err, dockerclient.ErrInvalidArgument, "an uppercase repository must be refused")
	_, _, err = f.ContainerCreate(ctx, "", dockerclient.ContainerConfig{Image: "mongo:8", ExposedPorts: map[string]struct{}{"0/tcp": {}}})
	assert.ErrorIs(t, err, dockerclient.ErrInvalidArgument, "port 0 must be refused")
	err = f.CopyToContainer(ctx, "c1", "/tmp", []dockerclient.File{{Name: "../escape"}})
	assert.ErrorIs(t, err, dockerclient.ErrInvalidArgument, "a name escaping the destination must be refused")
}

func TestFakeRemoveRunningNeedsForce(t *testing.T) {
	f := dockermock.NewFake(dockermock.WithImages("mongo:8"))
	ctx := context.Background()
	id, _, err := f.ContainerCreate(ctx, "", dockerclient.ContainerConfig{Image: "mongo:8"})
	require.NoError(t, err, "create")
	require.NoError(t, f.ContainerStart(ctx, id), "start")

	err = f.ContainerRemove(ctx, id, dockerclient.RemoveOptions{})
	assert.ErrorIs(t, err, dockerclient.ErrConflict, "removing a running container without Force is a conflict, as it is for a daemon")
	require.NoError(t, f.ContainerRemove(ctx, id, dockerclient.RemoveOptions{Force: true}), "Force removes it")
	assert.Empty(t, f.Containers(), "nothing is left behind")
}

func TestFakeTopNeedsARunningContainer(t *testing.T) {
	f := dockermock.NewFake(dockermock.WithImages("mongo:8"))
	ctx := context.Background()
	id, _, err := f.ContainerCreate(ctx, "", dockerclient.ContainerConfig{Image: "mongo:8", Cmd: []string{"--replSet", "rs0"}})
	require.NoError(t, err, "create")

	_, err = f.ContainerTop(ctx, id)
	assert.ErrorIs(t, err, dockerclient.ErrConflict, "a stopped container has no process table, which the daemon reports as a conflict")

	require.NoError(t, f.ContainerStart(ctx, id), "start")
	top, err := f.ContainerTop(ctx, id)
	require.NoError(t, err, "top on a running container")
	require.Len(t, top.Processes, 1, "the default process table has one row")
	assert.Contains(t, top.Processes[0][1], "--replSet", "the row reflects the container's command")
}

func TestFakeAssignsHostPorts(t *testing.T) {
	f := dockermock.NewFake(dockermock.WithImages("mongo:8"))
	ctx := context.Background()
	cfg := dockerclient.ContainerConfig{
		Image: "mongo:8",
		HostConfig: &dockerclient.HostConfig{PortBindings: map[string][]dockerclient.PortBinding{
			"27017/tcp": {{HostIP: "127.0.0.1", HostPort: ""}},
		}},
	}
	first, _, err := f.ContainerCreate(ctx, "", cfg)
	require.NoError(t, err, "first create")
	second, _, err := f.ContainerCreate(ctx, "", cfg)
	require.NoError(t, err, "second create")

	a, err := f.ContainerInspect(ctx, first)
	require.NoError(t, err, "inspect first")
	b, err := f.ContainerInspect(ctx, second)
	require.NoError(t, err, "inspect second")
	assert.NotEmpty(t, a.HostPort("27017/tcp"), "an empty HostPort must be filled in, as the daemon does")
	assert.NotEqual(t, a.HostPort("27017/tcp"), b.HostPort("27017/tcp"),
		"two containers must not be given the same host port, or parallel tests would collide")
}

func TestFakePullFuncCanFail(t *testing.T) {
	f := dockermock.NewFake()
	f.PullFunc = func(ref string) error {
		return &dockerclient.PullError{Ref: ref, Message: "manifest unknown"}
	}
	err := f.ImagePull(context.Background(), "mongo:nope")
	assert.ErrorIs(t, err, dockerclient.ErrPull, "PullFunc decides the outcome of a pull")
	_, err = f.ImageInspect(context.Background(), "mongo:nope")
	assert.True(t, dockerclient.IsNotFound(err), "a failed pull must not record the image as present")
}

func TestDaemonRoutingIgnoresVersionPrefix(t *testing.T) {
	d := dockermock.NewDaemon()
	t.Cleanup(d.Close)
	d.ServeDefaults()

	c, err := dockerapi.New(dockerapi.WithHost(d.Host()))
	require.NoError(t, err, "a client against the fake daemon must construct")
	info, err := c.ContainerInspect(context.Background(), dockermock.DefaultContainerID)
	require.NoError(t, err, "inspect through the versioned path must match the unversioned route")
	assert.Equal(t, dockermock.DefaultContainerName, info.Name, "the default route answers with the documented name")

	reqs := d.Requests()
	require.Len(t, reqs, 2, "version negotiation, then the inspect")
	assert.Equal(t, "/version", reqs[0].RawPath, "negotiation is unversioned")
	assert.Equal(t, "/v1.44"+"/containers/"+dockermock.DefaultContainerID+"/json", reqs[1].RawPath, "later requests carry the negotiated version")
	assert.Equal(t, "/containers/"+dockermock.DefaultContainerID+"/json", reqs[1].Path, "Path has the version prefix removed for matching")
}

func TestDaemonUnmatchedRouteIs404(t *testing.T) {
	d := dockermock.NewDaemon(dockermock.OverTCP())
	t.Cleanup(d.Close)
	d.ServeVersion(dockermock.DefaultAPIVersion, dockermock.DefaultMinAPIVersion)

	c, err := dockerapi.New(dockerapi.WithHost(d.Host()))
	require.NoError(t, err, "client construction")
	_, err = c.ContainerInspect(context.Background(), "abc")
	assert.True(t, dockerclient.IsNotFound(err), "an unregistered route answers 404 like the daemon does, got %v", err)
}

func TestDaemonHandleReplacesARoute(t *testing.T) {
	d := dockermock.NewDaemon()
	t.Cleanup(d.Close)
	d.ServeDefaults()
	// Re-registering must replace, so a test can override one default.
	d.Handle("GET", "/containers/{id}/json", func(w http.ResponseWriter, r *http.Request) {
		dockermock.Error(w, http.StatusConflict, "busy with "+dockermock.PathParam(r, "id"))
	})

	c, err := dockerapi.New(dockerapi.WithHost(d.Host()))
	require.NoError(t, err, "client construction")
	_, err = c.ContainerInspect(context.Background(), "abc")
	assert.ErrorIs(t, err, dockerclient.ErrConflict, "the replacement route decides the answer")
	assert.Contains(t, err.Error(), "busy with abc", "the path parameter reaches the handler")
}

func TestDaemonResetForgetsRequests(t *testing.T) {
	d := dockermock.NewDaemon()
	t.Cleanup(d.Close)
	d.ServeDefaults()
	c, err := dockerapi.New(dockerapi.WithHost(d.Host()))
	require.NoError(t, err, "client construction")
	require.NoError(t, c.ContainerStart(context.Background(), "abc"), "start")
	require.NotEmpty(t, d.Requests(), "requests are recorded")

	d.Reset()
	assert.Empty(t, d.Requests(), "Reset forgets recorded requests")
	require.NoError(t, c.ContainerStart(context.Background(), "abc"), "the daemon still serves after a reset")
	assert.Len(t, d.Requests(), 1, "recording resumes, and negotiation is not repeated")
}
