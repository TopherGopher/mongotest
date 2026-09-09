package dockerapi

import (
	"archive/tar"
	"bytes"
	"context"
	"io"
	"net/http"
	"testing"

	"github.com/tophergopher/mongotest/internal/fakedaemon"
)

func TestCopyToContainerSendsTar(t *testing.T) {
	fd, c := newImageClient(t)
	fd.Handle("PUT", "/containers/{id}/archive", func(w http.ResponseWriter, r *http.Request) {
		if fakedaemon.PathParam(r, "id") != "abc" {
			fakedaemon.Error(w, 404, "No such container")
			return
		}
		w.WriteHeader(200)
	})
	files := []File{
		{Name: "mongo-tls/server.pem", Mode: 0o644, Content: []byte("cert+key")},
		{Name: "mongo-tls/ca.pem", Mode: 0o600, Content: []byte("ca")},
	}
	if err := c.CopyToContainer(context.Background(), "abc", "/etc", files); err != nil {
		t.Fatal(err)
	}
	r := fd.Requests()[1]
	if r.Method != "PUT" || r.Path != "/containers/abc/archive" || r.Query.Get("path") != "/etc" {
		t.Fatalf("request %s %s %v", r.Method, r.Path, r.Query)
	}
	if ct := r.Header.Get("Content-Type"); ct != "application/x-tar" {
		t.Fatalf("content-type %q", ct)
	}
	tr := tar.NewReader(bytes.NewReader(r.Body))
	type entry struct {
		name string
		mode int64
		typ  byte
		body string
	}
	var got []entry
	for {
		h, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		b, _ := io.ReadAll(tr)
		got = append(got, entry{h.Name, h.Mode, h.Typeflag, string(b)})
	}
	want := []entry{
		{"mongo-tls/", 0o755, tar.TypeDir, ""},
		{"mongo-tls/server.pem", 0o644, tar.TypeReg, "cert+key"},
		{"mongo-tls/ca.pem", 0o600, tar.TypeReg, "ca"},
	}
	if len(got) != len(want) {
		t.Fatalf("entries %+v", got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("entry %d = %+v, want %+v", i, got[i], want[i])
		}
	}
}

func TestCopyToContainerEncodesPathQuery(t *testing.T) {
	fd, c := newImageClient(t)
	fd.Handle("PUT", "/containers/{id}/archive", func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(200) })
	if err := c.CopyToContainer(context.Background(), "abc", "/tmp/with space", []File{{Name: "x", Content: []byte("1")}}); err != nil {
		t.Fatal(err)
	}
	if got := fd.Requests()[1].Query.Get("path"); got != "/tmp/with space" {
		t.Fatalf("path query %q", got)
	}
}

func TestCopyToContainerErrors(t *testing.T) {
	fd, c := newImageClient(t)
	fd.Handle("PUT", "/containers/{id}/archive", func(w http.ResponseWriter, r *http.Request) {
		switch fakedaemon.PathParam(r, "id") {
		case "gone":
			fakedaemon.Error(w, 404, "No such container: gone")
		default:
			fakedaemon.Error(w, 400, "extraction point is not a directory")
		}
	})
	if err := c.CopyToContainer(context.Background(), "gone", "/tmp", []File{{Name: "x"}}); !IsNotFound(err) {
		t.Fatalf("want not found, got %v", err)
	}
	err := c.CopyToContainer(context.Background(), "abc", "/etc/passwd", []File{{Name: "x"}})
	if err == nil || IsNotFound(err) {
		t.Fatalf("want 400 error, got %v", err)
	}
	if err := c.CopyToContainer(context.Background(), "abc", "/tmp", nil); err == nil {
		t.Fatal("no files must be rejected before any request")
	}
	if err := c.CopyToContainer(context.Background(), "abc", "/tmp", []File{{Name: "/abs"}}); err == nil {
		t.Fatal("absolute names must be rejected")
	}
}

func TestCopyToContainerDefaultMode(t *testing.T) {
	fd, c := newImageClient(t)
	fd.Handle("PUT", "/containers/{id}/archive", func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(200) })
	if err := c.CopyToContainer(context.Background(), "abc", "/tmp", []File{{Name: "script.js", Content: []byte("1")}}); err != nil {
		t.Fatal(err)
	}
	tr := tar.NewReader(bytes.NewReader(fd.Requests()[1].Body))
	h, err := tr.Next()
	if err != nil {
		t.Fatal(err)
	}
	if h.Mode != 0o644 || h.Name != "script.js" || h.Size != 1 {
		t.Fatalf("header %+v", h)
	}
}
