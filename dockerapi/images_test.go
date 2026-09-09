package dockerapi

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/tophergopher/mongotest/internal/fakedaemon"
)

func newImageClient(t *testing.T) (*fakedaemon.Server, *Client) {
	t.Helper()
	fd := fakedaemon.New(t)
	fd.ServeVersion("1.54", "1.40")
	c, err := New(WithHost(fd.Host()))
	if err != nil {
		t.Fatal(err)
	}
	return fd, c
}

func pullRequests(fd *fakedaemon.Server) []fakedaemon.RecordedRequest {
	var out []fakedaemon.RecordedRequest
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
		if err := c.ImagePull(context.Background(), tc.ref); err != nil {
			t.Fatalf("%s: %v", tc.ref, err)
		}
		r := pullRequests(fd)[i]
		if r.Method != "POST" || r.Query.Get("fromImage") != tc.fromImage || r.Query.Get("tag") != tc.tag {
			t.Errorf("%s: query %v", tc.ref, r.Query)
		}
		if r.Header.Get("X-Registry-Auth") != "" {
			t.Errorf("%s: unexpected auth header", tc.ref)
		}
	}
}

func TestImagePullErrorLineInStream(t *testing.T) {
	fd, c := newImageClient(t)
	fd.Handle("POST", "/images/create", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintln(w, `{"status":"Pulling from library/mongo","id":"nope"}`)
		fmt.Fprintln(w, `{"errorDetail":{"message":"manifest for mongo:nope not found: manifest unknown"},"error":"manifest for mongo:nope not found: manifest unknown"}`)
	})
	err := c.ImagePull(context.Background(), "mongo:nope")
	if err == nil || !strings.Contains(err.Error(), "manifest for mongo:nope not found") || !strings.Contains(err.Error(), "mongo:nope") {
		t.Fatalf("err = %v", err)
	}
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
	if err := c.ImagePull(context.Background(), "mongo:8"); err != nil {
		t.Fatal(err)
	}
	select {
	case <-finished:
	default:
		t.Fatal("pull returned before the daemon finished writing")
	}
}

func TestImagePullHTTPError(t *testing.T) {
	fd, c := newImageClient(t)
	fd.Handle("POST", "/images/create", func(w http.ResponseWriter, r *http.Request) {
		fakedaemon.Error(w, 500, "Get https://registry-1.docker.io/v2/: dial tcp: i/o timeout")
	})
	err := c.ImagePull(context.Background(), "mongo:8")
	var se *StatusError
	if !errors.As(err, &se) || se.StatusCode != 500 || !strings.Contains(err.Error(), "i/o timeout") {
		t.Fatalf("err = %v", err)
	}
}

func TestImageInspect(t *testing.T) {
	fd, c := newImageClient(t)
	fd.Handle("GET", "/images/{ref}/json", func(w http.ResponseWriter, r *http.Request) {
		if fakedaemon.PathParam(r, "ref") != "mongo:8" {
			fakedaemon.Error(w, 404, "No such image: "+fakedaemon.PathParam(r, "ref"))
			return
		}
		io.WriteString(w, `{"Id":"sha256:41c3b7abb48e","RepoTags":["mongo:8","mongo:8.3.8"],"Architecture":"amd64","Os":"linux","Size":831000000}`)
	})
	img, err := c.ImageInspect(context.Background(), "mongo:8")
	if err != nil {
		t.Fatal(err)
	}
	if img.ID != "sha256:41c3b7abb48e" || len(img.RepoTags) != 2 || img.Architecture != "amd64" || img.OS != "linux" {
		t.Fatalf("decoded %+v", img)
	}
	_, err = c.ImageInspect(context.Background(), "mongo:nope")
	if !IsNotFound(err) {
		t.Fatalf("want ErrNotFound, got %v", err)
	}
	if !strings.Contains(err.Error(), "No such image: mongo:nope") {
		t.Fatalf("daemon message lost: %v", err)
	}
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
	if _, err := c.ImageInspect(context.Background(), "docker.io/library/mongo:8"); err != nil {
		t.Fatal(err)
	}
	if got != "/v1.44/images/docker.io/library/mongo:8/json" {
		t.Fatalf("unexpected path %q", got)
	}
}
