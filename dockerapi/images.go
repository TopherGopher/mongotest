package dockerapi

import (
	"bufio"
	"context"
	"encoding/json/v2"
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

// pullMessage is one line of the pull progress stream.
type pullMessage struct {
	Status      string          `json:"status"`
	Error       string          `json:"error"`
	ErrorDetail pullErrorDetail `json:"errorDetail"`
}

// pullErrorDetail is the structured error inside a pullMessage.
type pullErrorDetail struct {
	Message string `json:"message"`
}

// ImagePull pulls ref (for example "mongo:8") from its registry without
// credentials. It returns once the daemon has finished the pull; the JSON
// progress stream is consumed line by line and any error reported inside it
// is returned as a PullError.
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
	return drainPullStream(bufio.NewScanner(resp.Body), ref)
}

// drainPullStream reads the daemon's newline-delimited JSON progress
// messages to EOF and returns the first error message found, if any.
func drainPullStream(sc *bufio.Scanner, ref string) error {
	var firstErr error
	// A nil initial buffer lets the scanner start small and grow only for
	// the rare long line; sizing it at 64 KiB up front cost that much per
	// pull, which BenchmarkImagePull made obvious.
	sc.Buffer(nil, 1024*1024)
	for sc.Scan() {
		line := sc.Bytes()
		if len(strings.TrimSpace(string(line))) == 0 {
			continue
		}
		var msg pullMessage
		if err := json.Unmarshal(line, &msg); err != nil {
			continue // progress lines we cannot parse are not fatal
		}
		if firstErr == nil {
			if m := msg.ErrorDetail.Message; m != "" {
				firstErr = &PullError{Ref: ref, Message: m}
			} else if msg.Error != "" {
				firstErr = &PullError{Ref: ref, Message: msg.Error}
			}
		}
	}
	if err := sc.Err(); err != nil && firstErr == nil {
		return &PullError{Ref: ref, Message: "the progress stream could not be read", Err: err}
	}
	return firstErr
}

// ImageInspect returns metadata for a local image, by reference ("mongo:8")
// or id ("sha256:..."). A missing image yields an error matching
// ErrNotFound.
func (c *Client) ImageInspect(ctx context.Context, ref string) (ImageInspect, error) {
	var out ImageInspect
	ref, err := checkImageRefOrID(ref)
	if err != nil {
		return out, err
	}
	err = c.getJSON(ctx, "/images/"+ref+"/json", &out)
	return out, err
}
