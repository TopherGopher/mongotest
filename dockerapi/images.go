package dockerapi

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
)

// ImageInspect is the subset of the daemon's image description we use.
type ImageInspect struct {
	ID           string   `json:"Id"`
	RepoTags     []string `json:"RepoTags"`
	Architecture string   `json:"Architecture"`
	OS           string   `json:"Os"`
}

// ImagePull pulls ref (for example "mongo:8") from its registry without
// credentials. It returns once the daemon has finished the pull; the JSON
// progress stream is drained and any error reported inside it is returned.
func (c *Client) ImagePull(ctx context.Context, ref string) error {
	r, err := parseImageRef(ref)
	if err != nil {
		return err
	}
	// Like the official client: the tag parameter carries the digest for a
	// digest reference, otherwise the tag ("latest" when none was given).
	q := url.Values{"fromImage": {r.name}}
	if r.digest != "" {
		q.Set("tag", r.digest)
	} else {
		q.Set("tag", r.tag)
	}
	resp, err := c.do(ctx, http.MethodPost, "/images/create", q, nil)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return newStatusError(resp, http.MethodPost, "/images/create")
	}
	return drainPullStream(resp.Body, ref)
}

// drainPullStream reads the daemon's newline-delimited JSON progress
// messages to EOF and returns the first error message found, if any.
func drainPullStream(r io.Reader, ref string) error {
	var firstErr error
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 64*1024), 1024*1024)
	for sc.Scan() {
		line := sc.Bytes()
		if len(strings.TrimSpace(string(line))) == 0 {
			continue
		}
		var msg struct {
			Status      string `json:"status"`
			Error       string `json:"error"`
			ErrorDetail struct {
				Message string `json:"message"`
			} `json:"errorDetail"`
		}
		if err := json.Unmarshal(line, &msg); err != nil {
			continue // progress lines we cannot parse are not fatal
		}
		if firstErr == nil {
			if m := msg.ErrorDetail.Message; m != "" {
				firstErr = fmt.Errorf("dockerapi: pull %s: %s", ref, m)
			} else if msg.Error != "" {
				firstErr = fmt.Errorf("dockerapi: pull %s: %s", ref, msg.Error)
			}
		}
	}
	if err := sc.Err(); err != nil && firstErr == nil {
		return fmt.Errorf("dockerapi: pull %s: reading progress stream: %w", ref, err)
	}
	return firstErr
}

// ImageInspect returns metadata for a local image. A missing image yields
// an error matching ErrNotFound.
func (c *Client) ImageInspect(ctx context.Context, ref string) (ImageInspect, error) {
	ref, err := checkImageRefOrID(ref)
	if err != nil {
		return ImageInspect{}, err
	}
	path := "/images/" + ref + "/json"
	resp, err := c.do(ctx, http.MethodGet, path, nil, nil)
	if err != nil {
		return ImageInspect{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return ImageInspect{}, newStatusError(resp, http.MethodGet, path)
	}
	var out ImageInspect
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return ImageInspect{}, fmt.Errorf("dockerapi: decode image inspect for %s: %w", ref, err)
	}
	return out, nil
}
