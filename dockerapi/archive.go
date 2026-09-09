package dockerapi

import (
	"archive/tar"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"net/http"
	"net/url"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// File is one regular file to place in a container with CopyToContainer.
type File struct {
	// Name is the path relative to the destination directory. It may contain
	// directories ("mongo-tls/server.pem"); they are created as needed.
	Name string
	// Mode holds the unix permission bits (for example 0o644); 0 means
	// 0o644. Only permission bits are accepted: type bits such as
	// fs.ModeDir and the setuid, setgid and sticky bits are rejected.
	Mode    fs.FileMode
	Content []byte
}

// CopyToContainer writes files under destDir inside the container by
// uploading a tar archive to PUT /containers/{id}/archive. destDir must
// already exist in the container; intermediate directories named in the
// file paths are created. It works on a container that has been created but
// not yet started, which is how TLS material is injected before mongod runs.
func (c *Client) CopyToContainer(ctx context.Context, id, destDir string, files []File) error {
	id, err := checkID("container", id)
	if err != nil {
		return err
	}
	destDir = filepath.ToSlash(destDir)
	if !strings.HasPrefix(destDir, "/") || strings.Contains(destDir, "/../") || strings.HasSuffix(destDir, "/..") {
		return fmt.Errorf("dockerapi: CopyToContainer: destination %q must be an absolute container path", destDir)
	}
	if len(files) == 0 {
		return errors.New("dockerapi: CopyToContainer: no files given")
	}
	archive, err := buildTar(files)
	if err != nil {
		return err
	}
	p := "/containers/" + id + "/archive"
	// noOverwriteDirNonDir mirrors the docker cp default: never replace a
	// directory with a file or vice versa.
	q := url.Values{"path": {destDir}, "noOverwriteDirNonDir": {"true"}}
	resp, err := c.doRaw(ctx, http.MethodPut, p, q, bytes.NewReader(archive), true, "application/x-tar")
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return newStatusError(resp, http.MethodPut, p)
	}
	return nil
}

// buildTar produces an in-memory tar archive with directory entries for
// every intermediate directory followed by the files, in the order given.
func buildTar(files []File) ([]byte, error) {
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	now := time.Now()

	dirs := map[string]bool{}
	var dirList []string
	for _, f := range files {
		name := path.Clean(f.Name)
		if name == "." || name == "" || strings.HasPrefix(name, "/") || strings.HasPrefix(name, "../") || name == ".." {
			return nil, fmt.Errorf("dockerapi: CopyToContainer: invalid file name %q (must be relative, without ..)", f.Name)
		}
		for d := path.Dir(name); d != "." && d != "/"; d = path.Dir(d) {
			if !dirs[d] {
				dirs[d] = true
				dirList = append(dirList, d)
			}
		}
	}
	sort.Strings(dirList) // parents sort before children
	for _, d := range dirList {
		if err := tw.WriteHeader(&tar.Header{Name: d + "/", Mode: 0o755, Typeflag: tar.TypeDir, ModTime: now}); err != nil {
			return nil, fmt.Errorf("dockerapi: tar dir %s: %w", d, err)
		}
	}
	for _, f := range files {
		mode := f.Mode
		if mode == 0 {
			mode = 0o644
		}
		if mode&^fs.ModePerm != 0 {
			return nil, fmt.Errorf("dockerapi: CopyToContainer: %s: mode %v has bits other than permissions (only 0o777 bits are allowed)", f.Name, mode)
		}
		hdr := &tar.Header{
			Name:     path.Clean(f.Name),
			Mode:     int64(mode.Perm()),
			Size:     int64(len(f.Content)),
			Typeflag: tar.TypeReg,
			ModTime:  now,
		}
		if err := tw.WriteHeader(hdr); err != nil {
			return nil, fmt.Errorf("dockerapi: tar header %s: %w", f.Name, err)
		}
		if _, err := tw.Write(f.Content); err != nil {
			return nil, fmt.Errorf("dockerapi: tar content %s: %w", f.Name, err)
		}
	}
	if err := tw.Close(); err != nil {
		return nil, fmt.Errorf("dockerapi: close tar: %w", err)
	}
	return buf.Bytes(), nil
}
