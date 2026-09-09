package dockerapi

import (
	"archive/tar"
	"bytes"
	"context"
	"io"
	"io/fs"
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tophergopher/mongotest/dockermock"
)

// tarEntry is one entry read back from an archive under test.
type tarEntry struct {
	name string
	mode int64
	typ  byte
	body string
}

func readTar(t *testing.T, b []byte) []tarEntry {
	t.Helper()
	tr := tar.NewReader(bytes.NewReader(b))
	var got []tarEntry
	for {
		h, err := tr.Next()
		if err == io.EOF {
			break
		}
		require.NoError(t, err, "the archive must be a well-formed tar stream")
		body, err := io.ReadAll(tr)
		require.NoError(t, err, "reading entry %s", h.Name)
		got = append(got, tarEntry{h.Name, h.Mode, h.Typeflag, string(body)})
	}
	return got
}

func TestWriteTarStreamsToWriter(t *testing.T) {
	var buf bytes.Buffer
	err := writeTar(&buf, []File{
		{Name: "mongo-tls/server.pem", Mode: 0o644, Content: []byte("cert+key")},
		{Name: "mongo-tls/ca.pem", Mode: 0o600, Content: []byte("ca")},
	})
	require.NoError(t, err, "writing a tar with two files under one directory")
	want := []tarEntry{
		{"mongo-tls/", 0o755, tar.TypeDir, ""},
		{"mongo-tls/server.pem", 0o644, tar.TypeReg, "cert+key"},
		{"mongo-tls/ca.pem", 0o600, tar.TypeReg, "ca"},
	}
	assert.Equal(t, want, readTar(t, buf.Bytes()), "the archive must hold the directory entry first, then the files in order with their modes and content")
}

func TestCopyToContainerSendsTarStream(t *testing.T) {
	fd, c := newImageClient(t)
	fd.Handle("PUT", "/containers/{id}/archive", func(w http.ResponseWriter, r *http.Request) {
		if dockermock.PathParam(r, "id") != "abc" {
			dockermock.Error(w, 404, "No such container")
			return
		}
		w.WriteHeader(200)
	})
	files := []File{
		{Name: "mongo-tls/server.pem", Mode: 0o644, Content: []byte("cert+key")},
		{Name: "mongo-tls/ca.pem", Mode: 0o600, Content: []byte("ca")},
	}
	require.NoError(t, c.CopyToContainer(context.Background(), "abc", "/etc", files), "copy against the fake daemon")
	r := fd.Requests()[1]
	assert.Equal(t, "PUT", r.Method, "archive upload is a PUT")
	assert.Equal(t, "/containers/abc/archive", r.Path, "the archive endpoint")
	assert.Equal(t, "/etc", r.Query.Get("path"), "the destination directory travels in the path query parameter")
	assert.Equal(t, "true", r.Query.Get("noOverwriteDirNonDir"), "the docker cp default guard against replacing a directory with a file must be sent")
	assert.Equal(t, "application/x-tar", r.Header.Get("Content-Type"), "the body must be labelled as a tar archive")
	// net/http strips the hop-by-hop Transfer-Encoding header on the server
	// side, so the absence of Content-Length is the observable proof that the
	// archive was streamed rather than buffered.
	assert.Empty(t, r.Header.Get("Content-Length"), "the archive is streamed, so no Content-Length is known up front")
	got := readTar(t, r.Body)
	require.Len(t, got, 3, "directory entry plus two files")
	assert.Equal(t, "mongo-tls/server.pem", got[1].name, "second entry is the first file")
	assert.Equal(t, "cert+key", got[1].body, "file content arrives intact")
}

func TestCopyArchiveToContainerTakesAnyReader(t *testing.T) {
	fd, c := newImageClient(t)
	fd.Handle("PUT", "/containers/{id}/archive", func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(200) })
	var buf bytes.Buffer
	require.NoError(t, writeTar(&buf, []File{{Name: "x", Content: []byte("1")}}), "building a caller-side archive")
	require.NoError(t, c.CopyArchiveToContainer(context.Background(), "abc", "/tmp", &buf), "a caller-provided tar stream is uploaded as is")
	assert.Equal(t, "x", readTar(t, fd.Requests()[1].Body)[0].name, "the caller's archive arrives unchanged")
	err := c.CopyArchiveToContainer(context.Background(), "abc", "/tmp", nil)
	assert.ErrorIs(t, err, ErrInvalidArgument, "a nil reader is rejected before any request")
}

func TestCopyToContainerEncodesPathQuery(t *testing.T) {
	fd, c := newImageClient(t)
	fd.Handle("PUT", "/containers/{id}/archive", func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(200) })
	require.NoError(t, c.CopyToContainer(context.Background(), "abc", "/tmp/with space", []File{{Name: "x", Content: []byte("1")}}), "copy to a directory with a space")
	assert.Equal(t, "/tmp/with space", fd.Requests()[1].Query.Get("path"), "the destination must survive URL encoding")
}

func TestCopyToContainerErrors(t *testing.T) {
	fd, c := newImageClient(t)
	fd.Handle("PUT", "/containers/{id}/archive", func(w http.ResponseWriter, r *http.Request) {
		switch dockermock.PathParam(r, "id") {
		case "gone":
			dockermock.Error(w, 404, "No such container: gone")
		default:
			dockermock.Error(w, 400, "extraction point is not a directory")
		}
	})
	err := c.CopyToContainer(context.Background(), "gone", "/tmp", []File{{Name: "x"}})
	assert.True(t, IsNotFound(err), "an unknown container must be ErrNotFound, got %v", err)
	err = c.CopyToContainer(context.Background(), "abc", "/etc/passwd", []File{{Name: "x"}})
	require.Error(t, err, "copying into a file must fail")
	assert.False(t, IsNotFound(err), "a 400 must not look like not found")
	assert.Contains(t, err.Error(), "extraction point is not a directory", "the daemon's message is preserved")

	err = c.CopyToContainer(context.Background(), "abc", "/tmp", nil)
	assert.Same(t, ErrNoFiles, err, "no files returns the predeclared ErrNoFiles before any request")
	err = c.CopyToContainer(context.Background(), "abc", "/tmp", []File{{Name: "/abs"}})
	assert.ErrorIs(t, err, ErrInvalidArgument, "absolute file names are rejected before any request")
	assert.Contains(t, err.Error(), "relative to the destination directory", "the error explains what a valid name looks like")
}

func TestCopyToContainerDefaultMode(t *testing.T) {
	fd, c := newImageClient(t)
	fd.Handle("PUT", "/containers/{id}/archive", func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(200) })
	require.NoError(t, c.CopyToContainer(context.Background(), "abc", "/tmp", []File{{Name: "script.js", Content: []byte("1")}}), "copy with the default mode")
	got := readTar(t, fd.Requests()[1].Body)
	require.Len(t, got, 1, "a single file without directories yields one entry")
	assert.Equal(t, int64(0o644), got[0].mode, "a zero Mode defaults to 0o644")
	assert.Equal(t, "script.js", got[0].name, "the name is kept")
}

func TestFileModeValidation(t *testing.T) {
	err := validateFiles([]File{{Name: "x", Mode: fs.ModeDir | 0o755}})
	assert.ErrorIs(t, err, ErrInvalidArgument, "type bits in Mode must be rejected")
	err = validateFiles([]File{{Name: "x", Mode: fs.ModeSetuid | 0o755}})
	assert.ErrorIs(t, err, ErrInvalidArgument, "the setuid bit must be rejected")
	err = validateFiles([]File{{Name: "x", Mode: 0o1777}})
	assert.ErrorIs(t, err, ErrInvalidArgument, "the sticky bit written as an octal literal must be rejected")
	assert.Contains(t, err.Error(), "0o644", "the mode error must show an example of a valid value")
	assert.NoError(t, validateFiles([]File{{Name: "x", Mode: 0o600}}), "plain permission bits are accepted")
}
