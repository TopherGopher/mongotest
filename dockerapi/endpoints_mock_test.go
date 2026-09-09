package dockerapi

import (
	"context"
	"errors"
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Per-endpoint error handling and request shape, in the style of the moby
// client's own tests: an in-process RoundTripper stands in for the daemon.

func TestMockContainerStart(t *testing.T) {
	ctx := context.Background()
	c := newMockClient(t, errorMock(http.StatusInternalServerError, "Server error"))
	err := c.ContainerStart(ctx, "nothing")
	var se *StatusError
	require.True(t, errors.As(err, &se), "a 500 on start must be a StatusError, got %v", err)
	assert.Equal(t, 500, se.StatusCode, "the status code is carried")
	assert.False(t, IsNotFound(err), "a 500 must not look like not found")

	c = newMockClient(t, func(req *http.Request) (*http.Response, error) {
		if err := assertRequest(req, http.MethodPost, "/containers/container_id/start"); err != nil {
			return nil, err
		}
		if req.Header.Get("Content-Type") != "" {
			return nil, errors.New("start must not send a body")
		}
		return mockStatus(http.StatusNoContent)(req)
	})
	require.NoError(t, c.ContainerStart(ctx, "container_id"), "start with the expected request shape")
	require.NoError(t, c.ContainerStart(ctx, "/container_id"), "a leading slash from inspect names must be tolerated")
}

func TestMockContainerRemove(t *testing.T) {
	ctx := context.Background()
	c := newMockClient(t, func(req *http.Request) (*http.Response, error) {
		if err := assertRequest(req, http.MethodDelete, "/containers/container_id"); err != nil {
			return nil, err
		}
		if err := assertQuery(req, "force=1&v=1"); err != nil {
			return nil, err
		}
		return mockStatus(http.StatusNoContent)(req)
	})
	require.NoError(t, c.ContainerRemove(ctx, "container_id", RemoveOptions{Force: true, RemoveVolumes: true}), "remove with force and volumes")
	c = newMockClient(t, errorMock(http.StatusConflict, "removal of container x is already in progress"))
	assert.ErrorIs(t, c.ContainerRemove(ctx, "x", RemoveOptions{}), ErrConflict, "a 409 on remove must be ErrConflict")
}

func TestMockContainerCreate(t *testing.T) {
	ctx := context.Background()
	c := newMockClient(t, func(req *http.Request) (*http.Response, error) {
		if err := assertRequest(req, http.MethodPost, "/containers/create"); err != nil {
			return nil, err
		}
		if err := assertQuery(req, "name=mongotest-1"); err != nil {
			return nil, err
		}
		var body ContainerConfig
		if err := decodeBody(req, &body); err != nil {
			return nil, err
		}
		if body.Image != "mongo:8" {
			return nil, errors.New("image missing from body")
		}
		return mockJSON(http.StatusCreated, createResponse{ID: "abc"})(req)
	})
	id, _, err := c.ContainerCreate(ctx, "/mongotest-1", ContainerConfig{Image: "mongo:8"})
	require.NoError(t, err, "create with a leading-slash name")
	assert.Equal(t, "abc", id, "the id is returned")

	c = newMockClient(t, mockJSON(http.StatusOK, createResponse{ID: "abc"}))
	_, _, err = c.ContainerCreate(ctx, "", ContainerConfig{Image: "mongo:8"})
	assert.ErrorIs(t, err, ErrDaemonResponse, "a 200 where 201 is documented must be reported, not silently accepted")

	c = newMockClient(t, errorMock(http.StatusBadRequest, "invalid port specification"))
	_, _, err = c.ContainerCreate(ctx, "", ContainerConfig{Image: "mongo:8"})
	require.Error(t, err, "a 400 must be an error")
	assert.False(t, IsNotFound(err), "a 400 must not look like not found")
	assert.Contains(t, err.Error(), "invalid port specification", "the daemon's message is preserved")
	assert.Contains(t, err.Error(), "names the field to fix", "a 400 carries a hint")
}

func TestMockContainerInspectAndTop(t *testing.T) {
	ctx := context.Background()
	c := newMockClient(t, func(req *http.Request) (*http.Response, error) {
		switch {
		case assertRequest(req, http.MethodGet, "/containers/cid/json") == nil:
			return mockJSON(200, ContainerInspect{ID: "cid"})(req)
		case assertRequest(req, http.MethodGet, "/containers/cid/top") == nil:
			return mockJSON(200, Top{Titles: []string{"PID"}, Processes: [][]string{{"1"}}})(req)
		}
		return nil, errors.New("unexpected " + req.Method + " " + req.URL.Path)
	})
	info, err := c.ContainerInspect(ctx, "cid")
	require.NoError(t, err, "inspect")
	assert.Equal(t, "cid", info.ID, "inspect decodes the id")
	top, err := c.ContainerTop(ctx, "cid")
	require.NoError(t, err, "top")
	assert.Len(t, top.Processes, 1, "top decodes the process rows")

	c = newMockClient(t, mockStatus(200))
	_, err = c.ContainerInspect(ctx, "cid")
	require.ErrorIs(t, err, ErrDaemonResponse, "an empty 200 body is a decode failure, not a zero value")
}

func TestMockImagePullAndInspect(t *testing.T) {
	ctx := context.Background()
	c := newMockClient(t, func(req *http.Request) (*http.Response, error) {
		if err := assertRequest(req, http.MethodPost, "/images/create"); err != nil {
			return nil, err
		}
		if err := assertQuery(req, "fromImage=mongo&tag=8"); err != nil {
			return nil, err
		}
		return mockStatus(http.StatusOK)(req) // empty progress stream is fine
	})
	require.NoError(t, c.ImagePull(ctx, "mongo:8"), "pull with the expected query")
	c = newMockClient(t, errorMock(http.StatusUnauthorized, "unauthorized: authentication required"))
	assert.ErrorIs(t, c.ImagePull(ctx, "private/img"), ErrUnauthorized, "a 401 on pull must be ErrUnauthorized")
	c = newMockClient(t, func(req *http.Request) (*http.Response, error) {
		if err := assertRequest(req, http.MethodGet, "/images/mongo:8/json"); err != nil {
			return nil, err
		}
		return mockJSON(200, ImageInspect{ID: "sha256:abc"})(req)
	})
	img, err := c.ImageInspect(ctx, "mongo:8")
	require.NoError(t, err, "image inspect")
	assert.Equal(t, "sha256:abc", img.ID, "the image id decodes")
}

func TestMockCopyToContainer(t *testing.T) {
	ctx := context.Background()
	c := newMockClient(t, func(req *http.Request) (*http.Response, error) {
		if err := assertRequest(req, http.MethodPut, "/containers/cid/archive"); err != nil {
			return nil, err
		}
		if err := assertQuery(req, "noOverwriteDirNonDir=true&path=%2Ftmp"); err != nil {
			return nil, err
		}
		if req.Header.Get("Content-Type") != "application/x-tar" {
			return nil, errors.New("content type")
		}
		return mockStatus(http.StatusOK)(req)
	})
	require.NoError(t, c.CopyToContainer(ctx, "cid", "/tmp", []File{{Name: "a", Content: []byte("1")}}), "copy with the expected request shape")
	c = newMockClient(t, errorMock(http.StatusBadRequest, "extraction point is not a directory"))
	err := c.CopyToContainer(ctx, "cid", "/etc/passwd", []File{{Name: "a"}})
	require.Error(t, err, "a 400 on copy must be an error")
	assert.False(t, IsNotFound(err), "a 400 must not look like not found")
}

func TestMockExecCreateAndInspect(t *testing.T) {
	ctx := context.Background()
	c := newMockClient(t, func(req *http.Request) (*http.Response, error) {
		switch {
		case assertRequest(req, http.MethodPost, "/containers/cid/exec") == nil:
			var body execCreateRequest
			if err := decodeBody(req, &body); err != nil {
				return nil, err
			}
			if !body.AttachStdout || !body.AttachStderr || body.Tty {
				return nil, errors.New("attach flags wrong")
			}
			return mockJSON(http.StatusCreated, execCreateResponse{ID: "eid"})(req)
		case assertRequest(req, http.MethodGet, "/exec/eid/json") == nil:
			return mockJSON(200, ExecInspect{ID: "eid", Running: false, ExitCode: 2})(req)
		}
		return nil, errors.New("unexpected " + req.Method + " " + req.URL.Path)
	})
	id, err := c.ExecCreate(ctx, "cid", ExecConfig{Cmd: []string{"true"}})
	require.NoError(t, err, "exec create")
	assert.Equal(t, "eid", id, "the exec id is returned")
	ins, err := c.ExecInspect(ctx, "eid")
	require.NoError(t, err, "exec inspect")
	assert.Equal(t, 2, ins.ExitCode, "the exit code decodes")
	c = newMockClient(t, errorMock(http.StatusNotFound, "No such container: cid"))
	_, err = c.ExecCreate(ctx, "cid", ExecConfig{Cmd: []string{"true"}})
	assert.True(t, IsNotFound(err), "a 404 on exec create must be ErrNotFound, got %v", err)
}

func TestMockNegotiateFailureIsSurfacedByEveryEndpoint(t *testing.T) {
	ctx := context.Background()
	rt := mockRoundTripper(func(req *http.Request) (*http.Response, error) {
		return errorMock(http.StatusInternalServerError, "version boom")(req)
	})
	c, err := New(WithHost("unix:///mock/docker.sock"), WithHTTPClient(&http.Client{Transport: rt}))
	require.NoError(t, err, "client construction")
	calls := map[string]func() error{
		"pull":          func() error { return c.ImagePull(ctx, "mongo") },
		"image inspect": func() error { _, err := c.ImageInspect(ctx, "mongo"); return err },
		"create":        func() error { _, _, err := c.ContainerCreate(ctx, "", ContainerConfig{Image: "mongo"}); return err },
		"start":         func() error { return c.ContainerStart(ctx, "cid") },
		"remove":        func() error { return c.ContainerRemove(ctx, "cid", RemoveOptions{}) },
		"inspect":       func() error { _, err := c.ContainerInspect(ctx, "cid"); return err },
		"top":           func() error { _, err := c.ContainerTop(ctx, "cid"); return err },
		"copy":          func() error { return c.CopyToContainer(ctx, "cid", "/tmp", []File{{Name: "a"}}) },
		"exec create":   func() error { _, err := c.ExecCreate(ctx, "cid", ExecConfig{Cmd: []string{"true"}}); return err },
		"exec inspect":  func() error { _, err := c.ExecInspect(ctx, "eid"); return err },
	}
	for name, call := range calls {
		err := call()
		require.Error(t, err, "%s: a failed negotiation must fail the call", name)
		assert.Contains(t, err.Error(), "version boom", "%s: the negotiation failure must be surfaced, not hidden", name)
	}
}
