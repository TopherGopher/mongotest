package dockerapi

import (
	"archive/tar"
	"context"
	"io"
	"net/http"
	"net/url"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// CopyToContainer writes files under destDir inside the container. The tar
// archive is streamed to the daemon as it is produced; nothing is buffered
// beyond the individual file contents the caller already holds. destDir must
// already exist in the container; intermediate directories named in the
// file paths are created. It works on a container that has been created but
// not yet started, which is how TLS material is injected before mongod runs.
func (c *Client) CopyToContainer(ctx context.Context, id, destDir string, files []File) error {
	if err := validateFiles(files); err != nil {
		return err
	}
	pr, pw := io.Pipe()
	go func() {
		// Any write error (including the daemon closing the request early)
		// ends the archive; a tar error is handed to the reader side so the
		// request fails with it.
		pw.CloseWithError(writeTar(pw, files))
	}()
	err := c.CopyArchiveToContainer(ctx, id, destDir, pr)
	// Make sure the writer goroutine is released if the request failed
	// before consuming the whole archive.
	_ = pr.Close()
	return err
}

// CopyArchiveToContainer uploads a tar archive read from archive and
// extracts it under destDir inside the container. Use it when the caller
// already has a tar stream (a file on disk, a pipe); CopyToContainer is the
// convenience wrapper for a handful of in-memory files. Directories are never
// overwritten by files or vice versa (noOverwriteDirNonDir).
func (c *Client) CopyArchiveToContainer(ctx context.Context, id, destDir string, archive io.Reader) error {
	id, err := checkID("container", id)
	if err != nil {
		return err
	}
	destDir = filepath.ToSlash(destDir)
	if !strings.HasPrefix(destDir, "/") || strings.Contains(destDir, "/../") || strings.HasSuffix(destDir, "/..") {
		return invalidArg("destination directory", destDir, "it must be an absolute path inside the container without '..'", `use a path like "/etc/mongo-tls" or "/tmp"`)
	}
	if archive == nil {
		return invalidArg("archive", "", "no tar stream was given", "pass an io.Reader that yields a tar archive")
	}
	p := "/containers/" + id + "/archive"
	q := url.Values{"path": {destDir}, "noOverwriteDirNonDir": {"true"}}
	resp, err := c.doRaw(ctx, http.MethodPut, p, q, archive, true, "application/x-tar")
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	switch {
	case resp.StatusCode == http.StatusOK:
		return nil
	case resp.StatusCode >= 400:
		return newStatusError(resp, http.MethodPut, p)
	}
	return unexpectedStatus(http.MethodPut, p, resp.StatusCode)
}

// writeTar streams a tar archive to w: a directory entry for every
// intermediate directory, then the files in the order given. Callers must
// validate files first.
func writeTar(w io.Writer, files []File) error {
	tw := tar.NewWriter(w)
	now := time.Now()

	dirs := map[string]bool{}
	var dirList []string
	for _, f := range files {
		for d := path.Dir(path.Clean(f.Name)); d != "." && d != "/"; d = path.Dir(d) {
			if !dirs[d] {
				dirs[d] = true
				dirList = append(dirList, d)
			}
		}
	}
	sort.Strings(dirList) // parents sort before children
	for _, d := range dirList {
		if err := tw.WriteHeader(&tar.Header{Name: d + "/", Mode: 0o755, Typeflag: tar.TypeDir, ModTime: now}); err != nil {
			return &StreamError{Problem: "writing tar directory entry " + d, Err: err}
		}
	}
	for _, f := range files {
		mode := f.Mode
		if mode == 0 {
			mode = 0o644
		}
		hdr := &tar.Header{
			Name:     path.Clean(f.Name),
			Mode:     int64(mode.Perm()),
			Size:     int64(len(f.Content)),
			Typeflag: tar.TypeReg,
			ModTime:  now,
		}
		if err := tw.WriteHeader(hdr); err != nil {
			return &StreamError{Problem: "writing tar header for " + f.Name, Err: err}
		}
		if _, err := tw.Write(f.Content); err != nil {
			return &StreamError{Problem: "writing tar content for " + f.Name, Err: err}
		}
	}
	if err := tw.Close(); err != nil {
		return &StreamError{Problem: "finishing tar archive", Err: err}
	}
	return nil
}
