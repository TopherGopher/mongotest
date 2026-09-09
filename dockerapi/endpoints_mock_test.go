package dockerapi

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"
)

// Per-endpoint error handling and request shape, in the style of the moby
// client's own tests. Each endpoint: a daemon 500 becomes a StatusError that
// is not ErrNotFound, and the versioned path and query are exactly right.

func TestMockContainerStart(t *testing.T) {
	ctx := context.Background()
	c := newMockClient(t, errorMock(http.StatusInternalServerError, "Server error"))
	err := c.ContainerStart(ctx, "nothing")
	var se *StatusError
	if !errors.As(err, &se) || se.StatusCode != 500 || IsNotFound(err) {
		t.Fatalf("err = %v", err)
	}

	c = newMockClient(t, func(req *http.Request) (*http.Response, error) {
		if err := assertRequest(req, http.MethodPost, "/containers/container_id/start"); err != nil {
			return nil, err
		}
		if req.Header.Get("Content-Type") != "" {
			return nil, errors.New("start must not send a body")
		}
		return mockStatus(http.StatusNoContent)(req)
	})
	if err := c.ContainerStart(ctx, "container_id"); err != nil {
		t.Fatal(err)
	}
	if err := c.ContainerStart(ctx, "/container_id"); err != nil {
		t.Fatalf("leading slash from inspect names must be tolerated: %v", err)
	}
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
	if err := c.ContainerRemove(ctx, "container_id", RemoveOptions{Force: true, RemoveVolumes: true}); err != nil {
		t.Fatal(err)
	}
	c = newMockClient(t, errorMock(http.StatusConflict, "removal of container x is already in progress"))
	if err := c.ContainerRemove(ctx, "x", RemoveOptions{}); !errors.Is(err, ErrConflict) {
		t.Fatalf("409 should be ErrConflict: %v", err)
	}
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
		var body map[string]any
		if err := decodeBody(req, &body); err != nil {
			return nil, err
		}
		if body["Image"] != "mongo:8" {
			return nil, errors.New("image missing from body")
		}
		return mockJSON(http.StatusCreated, map[string]any{"Id": "abc"})(req)
	})
	id, _, err := c.ContainerCreate(ctx, "/mongotest-1", ContainerConfig{Image: "mongo:8"})
	if err != nil || id != "abc" {
		t.Fatalf("id=%q err=%v", id, err)
	}
	// A 200 instead of the documented 201 is not treated as success blindly.
	c = newMockClient(t, mockJSON(http.StatusOK, map[string]any{"Id": "abc"}))
	if _, _, err := c.ContainerCreate(ctx, "", ContainerConfig{Image: "mongo:8"}); err == nil {
		t.Fatal("unexpected status must be reported")
	}
	c = newMockClient(t, errorMock(http.StatusBadRequest, "invalid port specification"))
	_, _, err = c.ContainerCreate(ctx, "", ContainerConfig{Image: "mongo:8"})
	if err == nil || IsNotFound(err) || !strings.Contains(err.Error(), "invalid port specification") {
		t.Fatalf("400 handling: %v", err)
	}
}

func TestMockContainerInspectAndTop(t *testing.T) {
	ctx := context.Background()
	c := newMockClient(t, func(req *http.Request) (*http.Response, error) {
		switch {
		case assertRequest(req, http.MethodGet, "/containers/cid/json") == nil:
			return mockJSON(200, map[string]any{"Id": "cid"})(req)
		case assertRequest(req, http.MethodGet, "/containers/cid/top") == nil:
			return mockJSON(200, map[string]any{"Titles": []string{"PID"}, "Processes": [][]string{{"1"}}})(req)
		}
		return nil, errors.New("unexpected " + req.Method + " " + req.URL.Path)
	})
	if info, err := c.ContainerInspect(ctx, "cid"); err != nil || info.ID != "cid" {
		t.Fatalf("inspect: %+v %v", info, err)
	}
	if top, err := c.ContainerTop(ctx, "cid"); err != nil || len(top.Processes) != 1 {
		t.Fatalf("top: %+v %v", top, err)
	}
	// Malformed JSON from the daemon is reported, not silently zeroed.
	c = newMockClient(t, func(req *http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 200, Body: http.NoBody, Request: req}, nil
	})
	if _, err := c.ContainerInspect(ctx, "cid"); err == nil || !strings.Contains(err.Error(), "decode") {
		t.Fatalf("empty body should be a decode error: %v", err)
	}
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
	if err := c.ImagePull(ctx, "mongo:8"); err != nil {
		t.Fatal(err)
	}
	c = newMockClient(t, errorMock(http.StatusUnauthorized, "unauthorized: authentication required"))
	if err := c.ImagePull(ctx, "private/img"); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("401 should be ErrUnauthorized: %v", err)
	}
	c = newMockClient(t, func(req *http.Request) (*http.Response, error) {
		if err := assertRequest(req, http.MethodGet, "/images/mongo:8/json"); err != nil {
			return nil, err
		}
		return mockJSON(200, map[string]any{"Id": "sha256:abc"})(req)
	})
	if img, err := c.ImageInspect(ctx, "mongo:8"); err != nil || img.ID != "sha256:abc" {
		t.Fatalf("inspect: %+v %v", img, err)
	}
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
	if err := c.CopyToContainer(ctx, "cid", "/tmp", []File{{Name: "a", Content: []byte("1")}}); err != nil {
		t.Fatal(err)
	}
	c = newMockClient(t, errorMock(http.StatusBadRequest, "extraction point is not a directory"))
	if err := c.CopyToContainer(ctx, "cid", "/etc/passwd", []File{{Name: "a"}}); err == nil || IsNotFound(err) {
		t.Fatalf("400: %v", err)
	}
}

func TestMockExecCreateAndInspect(t *testing.T) {
	ctx := context.Background()
	c := newMockClient(t, func(req *http.Request) (*http.Response, error) {
		switch {
		case assertRequest(req, http.MethodPost, "/containers/cid/exec") == nil:
			var body map[string]any
			if err := decodeBody(req, &body); err != nil {
				return nil, err
			}
			if body["AttachStdout"] != true || body["AttachStderr"] != true || body["Tty"] != false {
				return nil, errors.New("attach flags wrong")
			}
			return mockJSON(http.StatusCreated, map[string]string{"Id": "eid"})(req)
		case assertRequest(req, http.MethodGet, "/exec/eid/json") == nil:
			return mockJSON(200, map[string]any{"ID": "eid", "Running": false, "ExitCode": 2})(req)
		}
		return nil, errors.New("unexpected " + req.Method + " " + req.URL.Path)
	})
	id, err := c.ExecCreate(ctx, "cid", ExecConfig{Cmd: []string{"true"}})
	if err != nil || id != "eid" {
		t.Fatalf("create: %q %v", id, err)
	}
	if ins, err := c.ExecInspect(ctx, "eid"); err != nil || ins.ExitCode != 2 {
		t.Fatalf("inspect: %+v %v", ins, err)
	}
	c = newMockClient(t, errorMock(http.StatusNotFound, "No such container: cid"))
	if _, err := c.ExecCreate(ctx, "cid", ExecConfig{Cmd: []string{"true"}}); !IsNotFound(err) {
		t.Fatalf("404: %v", err)
	}
}

func TestMockNegotiateFailureIsSurfacedByEveryEndpoint(t *testing.T) {
	ctx := context.Background()
	rt := mockRoundTripper(func(req *http.Request) (*http.Response, error) {
		return errorMock(http.StatusInternalServerError, "version boom")(req)
	})
	c, err := New(WithHost("unix:///mock/docker.sock"), WithHTTPClient(&http.Client{Transport: rt}))
	if err != nil {
		t.Fatal(err)
	}
	calls := []func() error{
		func() error { return c.ImagePull(ctx, "mongo") },
		func() error { _, err := c.ImageInspect(ctx, "mongo"); return err },
		func() error { _, _, err := c.ContainerCreate(ctx, "", ContainerConfig{Image: "mongo"}); return err },
		func() error { return c.ContainerStart(ctx, "cid") },
		func() error { return c.ContainerRemove(ctx, "cid", RemoveOptions{}) },
		func() error { _, err := c.ContainerInspect(ctx, "cid"); return err },
		func() error { _, err := c.ContainerTop(ctx, "cid"); return err },
		func() error { return c.CopyToContainer(ctx, "cid", "/tmp", []File{{Name: "a"}}) },
		func() error { _, err := c.ExecCreate(ctx, "cid", ExecConfig{Cmd: []string{"true"}}); return err },
		func() error { _, err := c.ExecInspect(ctx, "eid"); return err },
	}
	for i, call := range calls {
		if err := call(); err == nil || !strings.Contains(err.Error(), "version boom") {
			t.Errorf("call %d: negotiation failure not surfaced: %v", i, err)
		}
	}
}
