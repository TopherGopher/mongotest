package mobyclient

import (
	"archive/tar"
	"context"
	"io"
	"net/http"
	"path"
	"sort"
	"time"

	"github.com/moby/moby/client"

	"github.com/tophergopher/mongotest/dockerclient"
)

// CopyToContainer writes files under destDir inside the container. The tar
// archive is streamed to the daemon as it is produced, through an io.Pipe,
// rather than built in memory first. It works on a container that has been
// created but not yet started, which is how material such as TLS
// certificates is put in place before the process runs.
func (c *Client) CopyToContainer(ctx context.Context, id, destDir string, files []dockerclient.File) error {
	if err := dockerclient.ValidateFiles(files); err != nil {
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
// already has a tar stream; CopyToContainer is the convenience wrapper for a
// handful of in-memory files. Directories are never overwritten by files or
// vice versa.
func (c *Client) CopyArchiveToContainer(ctx context.Context, id, destDir string, archive io.Reader) error {
	id, err := dockerclient.CheckID("container", id)
	if err != nil {
		return err
	}
	destDir, err = dockerclient.CheckDestDir(destDir)
	if err != nil {
		return err
	}
	if archive == nil {
		return dockerclient.InvalidArgument("archive", "", "no tar stream was given", "pass an io.Reader that yields a tar archive")
	}
	opts := client.CopyToContainerOptions{DestinationPath: destDir, Content: archive}
	if _, err := c.cli.CopyToContainer(ctx, id, opts); err != nil {
		return c.mapErr(http.MethodPut, "/containers/"+id+"/archive", err)
	}
	return nil
}

// writeTar streams a tar archive to w: a directory entry for every
// intermediate directory, then the files in the order given. Callers must
// validate files first.
func writeTar(w io.Writer, files []dockerclient.File) error {
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
			return &dockerclient.StreamError{Problem: "writing tar directory entry " + d, Err: err}
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
			return &dockerclient.StreamError{Problem: "writing tar header for " + f.Name, Err: err}
		}
		if _, err := tw.Write(f.Content); err != nil {
			return &dockerclient.StreamError{Problem: "writing tar content for " + f.Name, Err: err}
		}
	}
	if err := tw.Close(); err != nil {
		return &dockerclient.StreamError{Problem: "finishing tar archive", Err: err}
	}
	return nil
}
