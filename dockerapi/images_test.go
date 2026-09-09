package dockerapi

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tophergopher/mongotest/dockermock"
)

func newImageClient(t testing.TB) (*dockermock.Daemon, *Client) {
	t.Helper()
	fd := newDaemon(t)
	fd.ServeVersion("1.54", "1.40")
	c, err := New(WithHost(fd.Host()))
	require.NoError(t, err, "client construction against the fake daemon")
	return fd, c
}

func pullRequests(fd *dockermock.Daemon) []dockermock.Request {
	var out []dockermock.Request
	for _, r := range fd.Requests() {
		if r.Path == "/images/create" {
			out = append(out, r)
		}
	}
	return out
}

func TestImagePullQuery(t *testing.T) {
	fd, c := newImageClient(t)
	fd.Handle("POST", "/images/create", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintln(w, `{"status":"Pulling from library/mongo","id":"8"}`)
		fmt.Fprintln(w, `{"status":"Download complete","progressDetail":{"current":10,"total":10},"id":"a1"}`)
	})
	digest := "sha256:" + strings.Repeat("0", 64)
	cases := []struct{ ref, fromImage, tag string }{
		{"mongo:8", "mongo", "8"},
		{"mongo", "mongo", "latest"},
		{"docker.io/library/mongo:8", "docker.io/library/mongo", "8"},
		// Digest references send the digest in the tag parameter, exactly
		// like the official client does.
		{"mongo@" + digest, "mongo", digest},
		{"mongo:8@" + digest, "mongo", digest},
	}
	for i, tc := range cases {
		require.NoError(t, c.ImagePull(context.Background(), tc.ref), "pulling %q against the fake must succeed", tc.ref)
		r := pullRequests(fd)[i]
		assert.Equal(t, "POST", r.Method, "%s: pull is a POST", tc.ref)
		assert.Equal(t, tc.fromImage, r.Query.Get("fromImage"), "%s: fromImage carries the repository without tag or digest", tc.ref)
		assert.Equal(t, tc.tag, r.Query.Get("tag"), "%s: tag carries the tag, or the digest for digest references", tc.ref)
		assert.Empty(t, r.Header.Get("X-Registry-Auth"), "%s: public pulls send no registry credentials", tc.ref)
	}
}

func TestImagePullErrorLineInStream(t *testing.T) {
	fd, c := newImageClient(t)
	fd.Handle("POST", "/images/create", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintln(w, `{"status":"Pulling from library/mongo","id":"nope"}`)
		fmt.Fprintln(w, `{"errorDetail":{"message":"manifest for mongo:nope not found: manifest unknown"},"error":"manifest for mongo:nope not found: manifest unknown"}`)
	})
	err := c.ImagePull(context.Background(), "mongo:nope")
	require.ErrorIs(t, err, ErrPull, "an error line inside a 200 stream must surface as a pull failure")
	var pe *PullError
	require.True(t, errors.As(err, &pe), "callers can extract the typed PullError")
	assert.Equal(t, "mongo:nope", pe.Ref, "the reference is carried on the error")
	assert.Contains(t, err.Error(), "manifest for mongo:nope not found", "the daemon's message is preserved")
	assert.Contains(t, err.Error(), "Check the image name and tag", "the error must say what to check")
}

func TestImagePullDrainsWholeStream(t *testing.T) {
	fd, c := newImageClient(t)
	finished := make(chan struct{})
	fd.Handle("POST", "/images/create", func(w http.ResponseWriter, r *http.Request) {
		fl, _ := w.(http.Flusher)
		for i := 0; i < 50; i++ {
			fmt.Fprintf(w, `{"status":"Downloading","progressDetail":{"current":%d,"total":50},"id":"layer"}`+"\n", i)
			if fl != nil {
				fl.Flush()
			}
		}
		fmt.Fprintln(w, `{"status":"Status: Downloaded newer image for mongo:8"}`)
		close(finished)
	})
	require.NoError(t, c.ImagePull(context.Background(), "mongo:8"), "a clean progress stream is a successful pull")
	select {
	case <-finished:
	default:
		t.Fatal("ImagePull returned before the daemon finished writing the progress stream; the pull must be drained to completion")
	}
}

func TestImagePullHTTPError(t *testing.T) {
	fd, c := newImageClient(t)
	fd.Handle("POST", "/images/create", func(w http.ResponseWriter, r *http.Request) {
		dockermock.Error(w, 500, "Get https://registry-1.docker.io/v2/: dial tcp: i/o timeout")
	})
	err := c.ImagePull(context.Background(), "mongo:8")
	var se *StatusError
	require.True(t, errors.As(err, &se), "a non-200 pull response must be a StatusError")
	assert.Equal(t, 500, se.StatusCode, "the status code is carried")
	assert.Contains(t, err.Error(), "i/o timeout", "the daemon's message is preserved")
}

func TestImageInspect(t *testing.T) {
	fd, c := newImageClient(t)
	fd.Handle("GET", "/images/{ref}/json", func(w http.ResponseWriter, r *http.Request) {
		if dockermock.PathParam(r, "ref") != "mongo:8" {
			dockermock.Error(w, 404, "No such image: "+dockermock.PathParam(r, "ref"))
			return
		}
		io.WriteString(w, `{"Id":"sha256:41c3b7abb48e","RepoTags":["mongo:8","mongo:8.3.8"],"Architecture":"amd64","Os":"linux","Size":831000000}`)
	})
	img, err := c.ImageInspect(context.Background(), "mongo:8")
	require.NoError(t, err, "inspecting a present image")
	assert.Equal(t, "sha256:41c3b7abb48e", img.ID, "Id decodes into ID")
	assert.Equal(t, []string{"mongo:8", "mongo:8.3.8"}, img.RepoTags, "RepoTags decode")
	assert.Equal(t, "amd64", img.Architecture, "Architecture decodes")
	assert.Equal(t, "linux", img.OS, "Os decodes into OS")

	_, err = c.ImageInspect(context.Background(), "mongo:nope")
	require.True(t, IsNotFound(err), "a missing image must be ErrNotFound, got %v", err)
	assert.Contains(t, err.Error(), "No such image: mongo:nope", "the daemon's message is preserved")
}

func TestImageInspectRefWithSlashes(t *testing.T) {
	fd, c := newImageClient(t)
	var got string
	// The daemon routes /images/{name:.*}/json, so a reference containing
	// slashes is sent unescaped, exactly like the official client does.
	fd.Handle("GET", "/images/docker.io/library/mongo:8/json", func(w http.ResponseWriter, r *http.Request) {
		got = r.URL.Path
		io.WriteString(w, `{"Id":"x"}`)
	})
	_, err := c.ImageInspect(context.Background(), "docker.io/library/mongo:8")
	require.NoError(t, err, "inspect with a slashed reference")
	assert.Equal(t, "/v1.44/images/docker.io/library/mongo:8/json", got, "the reference must be sent unescaped in the path")
}
