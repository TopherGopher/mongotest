package dockerapi

import (
	"context"
	"encoding/json"
	"fmt"
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

// WithAPIVersion pins the Engine API version (for example "1.43") and skips
// negotiation. The DOCKER_API_VERSION environment variable has the same
// effect when this option is not given.
func WithAPIVersion(v string) Option {
	return func(c *Client) error {
		if !versionRe.MatchString(v) {
			return fmt.Errorf("dockerapi: invalid API version %q (want major.minor, for example 1.44)", v)
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
	var v struct {
		APIVersion    string `json:"ApiVersion"`
		MinAPIVersion string `json:"MinAPIVersion"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&v); err != nil {
		return fmt.Errorf("dockerapi: decode /version from %s: %w", c.host, err)
	}
	chosen, err := chooseVersion(v.APIVersion, v.MinAPIVersion)
	if err != nil {
		return err
	}
	c.apiVersion = chosen
	return nil
}

// chooseVersion applies the selection rule to the daemon's window.
func chooseVersion(serverMax, serverMin string) (string, error) {
	if !versionRe.MatchString(serverMax) {
		return "", fmt.Errorf("dockerapi: daemon reported unusable ApiVersion %q", serverMax)
	}
	chosen := PreferredAPIVersion
	if compareVersions(serverMax, chosen) < 0 {
		chosen = serverMax
	}
	if serverMin != "" && compareVersions(chosen, serverMin) < 0 {
		return "", fmt.Errorf("dockerapi: docker daemon requires API >= %s but this client supports at most %s", serverMin, PreferredAPIVersion)
	}
	if compareVersions(chosen, MinSupportedAPIVersion) < 0 {
		return "", fmt.Errorf("dockerapi: docker daemon only supports API <= %s but this client needs at least %s", serverMax, MinSupportedAPIVersion)
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

// versionState is embedded in Client.
type versionState struct {
	versionMu     sync.Mutex
	apiVersion    string
	versionPinned bool
}
