package dockerapi

import (
	"context"
	"encoding/json/v2"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"sync"
)

const (
	// PreferredAPIVersion is the newest Engine API version this client asks
	// for. It is lowered to the daemon's maximum when the daemon is older.
	PreferredAPIVersion = "1.44"
	// MinSupportedAPIVersion is the oldest Engine API version that has every
	// endpoint this client uses.
	MinSupportedAPIVersion = "1.24"
)

var versionRe = regexp.MustCompile(`^[0-9]+\.[0-9]+$`)

// versionResponse is the part of GET /version we read.
type versionResponse struct {
	APIVersion    string             `json:"ApiVersion"`
	MinAPIVersion string             `json:"MinAPIVersion"`
	Platform      versionPlatform    `json:"Platform"`
	Components    []versionComponent `json:"Components"`
}

// versionPlatform is the daemon's description of itself. Docker puts a
// product name here; Podman puts goos/goarch/distribution.
type versionPlatform struct {
	Name string `json:"Name"`
}

// versionComponent is one entry of the daemon's component list, which is
// what reliably distinguishes Docker from Podman.
type versionComponent struct {
	Name    string `json:"Name"`
	Version string `json:"Version"`
}

// versionState is embedded in Client and guarded by versionMu.
type versionState struct {
	versionMu     sync.Mutex
	apiVersion    string
	versionPinned bool
	runtime       Runtime
	product       string
}

// WithAPIVersion pins the Engine API version (for example "1.43") and skips
// negotiation. The DOCKER_API_VERSION environment variable has the same
// effect when this option is not given.
func WithAPIVersion(v string) Option {
	return func(c *Client) error {
		if !versionRe.MatchString(v) {
			return invalidArg("API version", v, "it must be major.minor", `use a value like "1.44", or unset DOCKER_API_VERSION to negotiate automatically`)
		}
		c.apiVersion = v
		c.versionPinned = true
		return nil
	}
}

// APIVersion returns the negotiated or pinned Engine API version, or "" if
// negotiation has not happened yet.
func (c *Client) APIVersion() string {
	c.versionMu.Lock()
	defer c.versionMu.Unlock()
	return c.apiVersion
}

// Negotiate picks the API version used for all requests by asking the
// daemon (GET /version) for the window it supports. It runs once; later
// calls are no-ops unless the earlier attempt failed. Public methods call it
// lazily, so callers rarely need to.
func (c *Client) Negotiate(ctx context.Context) error {
	c.versionMu.Lock()
	defer c.versionMu.Unlock()
	if c.apiVersion != "" {
		return nil
	}
	resp, err := c.request(ctx, http.MethodGet, "/version", nil, nil, false, "", false)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return newStatusError(resp, http.MethodGet, "/version")
	}
	var v versionResponse
	if err := json.UnmarshalRead(resp.Body, &v); err != nil {
		return decodeError(http.MethodGet, "/version", err)
	}
	// The runtime is recorded before the window is checked, so that a
	// version error can name the daemon that reported it.
	c.runtime, c.product = detectRuntime(v, resp.Header.Get(libpodVersionHeader))
	chosen, err := chooseVersion(v.APIVersion, v.MinAPIVersion, c.product)
	if err != nil {
		return err
	}
	c.apiVersion = chosen
	return nil
}

// chooseVersion applies the selection rule to the daemon's window. product
// is the daemon's own description of itself, for the error message.
func chooseVersion(serverMax, serverMin, product string) (string, error) {
	if !versionRe.MatchString(serverMax) {
		return "", &ResponseError{Method: http.MethodGet, Path: "/version", Problem: "the daemon reported an unusable ApiVersion " + strconv.Quote(serverMax)}
	}
	chosen := PreferredAPIVersion
	if compareVersions(serverMax, chosen) < 0 {
		chosen = serverMax
	}
	// The error carries both windows so the reader can see why they do not
	// overlap and which side to move.
	mismatch := &APIVersionError{
		Product:   product,
		ServerMin: serverMin, ServerMax: serverMax,
		ClientMin: MinSupportedAPIVersion, ClientMax: PreferredAPIVersion,
	}
	if serverMin != "" && compareVersions(chosen, serverMin) < 0 {
		return "", mismatch
	}
	if compareVersions(chosen, MinSupportedAPIVersion) < 0 {
		return "", mismatch
	}
	return chosen, nil
}

// compareVersions compares two "major.minor" strings numerically. An empty
// or malformed string sorts lowest.
func compareVersions(a, b string) int {
	am, an := splitVersion(a)
	bm, bn := splitVersion(b)
	switch {
	case am != bm:
		if am < bm {
			return -1
		}
		return 1
	case an != bn:
		if an < bn {
			return -1
		}
		return 1
	}
	return 0
}

func splitVersion(v string) (int, int) {
	parts := strings.SplitN(v, ".", 2)
	if len(parts) != 2 {
		return -1, -1
	}
	major, err1 := strconv.Atoi(parts[0])
	minor, err2 := strconv.Atoi(parts[1])
	if err1 != nil || err2 != nil {
		return -1, -1
	}
	return major, minor
}
