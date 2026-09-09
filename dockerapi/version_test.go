package dockerapi

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/tophergopher/mongotest/internal/fakedaemon"
)

func TestCompareVersions(t *testing.T) {
	tests := []struct {
		a, b string
		want int
	}{
		{"1.44", "1.44", 0},
		{"1.44", "1.9", 1}, // numeric, not lexical
		{"1.9", "1.44", -1},
		{"1.41", "1.44", -1},
		{"2.0", "1.99", 1},
		{"1.44", "", 1}, // empty sorts lowest
	}
	for _, tc := range tests {
		if got := compareVersions(tc.a, tc.b); got != tc.want {
			t.Errorf("compareVersions(%q,%q) = %d, want %d", tc.a, tc.b, got, tc.want)
		}
	}
}

// versionServer returns a fake daemon whose /version reports the given
// window and a probe route that records the versioned path used.
func versionServer(t *testing.T, apiVersion, minVersion string) *fakedaemon.Server {
	fd := fakedaemon.New(t)
	fd.Handle("GET", "/version", func(w http.ResponseWriter, _ *http.Request) {
		body := map[string]string{"Version": "29.3.1", "ApiVersion": apiVersion}
		if minVersion != "" {
			body["MinAPIVersion"] = minVersion
		}
		fakedaemon.JSON(w, 200, body)
	})
	fd.Handle("GET", "/_ping", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(200) })
	return fd
}

func ping(t *testing.T, c *Client) {
	t.Helper()
	resp, err := c.do(context.Background(), http.MethodGet, "/_ping", nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
}

func TestNegotiateChoosesPreferredWhenServerIsNewer(t *testing.T) {
	fd := versionServer(t, "1.54", "1.40")
	c, _ := New(WithHost(fd.Host()))
	if got := c.APIVersion(); got != "" {
		t.Fatalf("APIVersion before negotiation = %q, want empty", got)
	}
	ping(t, c)
	if got := c.APIVersion(); got != PreferredAPIVersion {
		t.Fatalf("APIVersion = %q, want %q", got, PreferredAPIVersion)
	}
	reqs := fd.Requests()
	if len(reqs) != 2 || reqs[0].RawPath != "/version" || reqs[1].RawPath != "/v"+PreferredAPIVersion+"/_ping" {
		t.Fatalf("requests: %+v", reqs)
	}
}

func TestNegotiateFallsBackToServerMax(t *testing.T) {
	fd := versionServer(t, "1.41", "1.24") // podman-like
	c, _ := New(WithHost(fd.Host()))
	ping(t, c)
	if c.APIVersion() != "1.41" {
		t.Fatalf("APIVersion = %q", c.APIVersion())
	}
	if p := fd.Requests()[1].RawPath; p != "/v1.41/_ping" {
		t.Fatalf("path %q", p)
	}
}

func TestNegotiateToleratesMissingMinVersion(t *testing.T) {
	fd := versionServer(t, "1.40", "")
	c, _ := New(WithHost(fd.Host()))
	ping(t, c)
	if c.APIVersion() != "1.40" {
		t.Fatalf("APIVersion = %q", c.APIVersion())
	}
}

func TestNegotiateServerMinimumTooHigh(t *testing.T) {
	fd := versionServer(t, "1.70", "1.60")
	c, _ := New(WithHost(fd.Host()))
	_, err := c.do(context.Background(), http.MethodGet, "/_ping", nil, nil)
	if err == nil {
		t.Fatal("expected error")
	}
	for _, want := range []string{"1.60", PreferredAPIVersion} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q should mention %s", err, want)
		}
	}
	if n := len(fd.Requests()); n != 1 {
		t.Fatalf("expected only the /version request, got %d", n)
	}
}

func TestNegotiateServerTooOld(t *testing.T) {
	fd := versionServer(t, "1.20", "")
	c, _ := New(WithHost(fd.Host()))
	err := c.Negotiate(context.Background())
	if err == nil || !strings.Contains(err.Error(), "1.20") || !strings.Contains(err.Error(), MinSupportedAPIVersion) {
		t.Fatalf("err = %v", err)
	}
}

func TestNegotiateHappensOnce(t *testing.T) {
	fd := versionServer(t, "1.54", "1.40")
	c, _ := New(WithHost(fd.Host()))
	for i := 0; i < 5; i++ {
		ping(t, c)
	}
	if err := c.Negotiate(context.Background()); err != nil {
		t.Fatal(err)
	}
	versions := 0
	for _, r := range fd.Requests() {
		if r.RawPath == "/version" {
			versions++
		}
	}
	if versions != 1 {
		t.Fatalf("/version called %d times, want 1", versions)
	}
}

func TestNegotiateRetriesAfterFailure(t *testing.T) {
	fd := fakedaemon.New(t)
	calls := 0
	fd.Handle("GET", "/version", func(w http.ResponseWriter, _ *http.Request) {
		calls++
		if calls == 1 {
			fakedaemon.Error(w, 500, "boom")
			return
		}
		fakedaemon.JSON(w, 200, map[string]string{"ApiVersion": "1.44"})
	})
	c, _ := New(WithHost(fd.Host()))
	if err := c.Negotiate(context.Background()); err == nil || !strings.Contains(err.Error(), "boom") {
		t.Fatalf("first negotiate should fail with daemon message, got %v", err)
	}
	if err := c.Negotiate(context.Background()); err != nil {
		t.Fatalf("second negotiate: %v", err)
	}
}

func TestPinnedVersionSkipsNegotiation(t *testing.T) {
	fd := versionServer(t, "1.54", "1.40")
	t.Run("WithAPIVersion", func(t *testing.T) {
		c, err := New(WithHost(fd.Host()), WithAPIVersion("1.43"))
		if err != nil {
			t.Fatal(err)
		}
		fd.Reset()
		ping(t, c)
		reqs := fd.Requests()
		if len(reqs) != 1 || reqs[0].RawPath != "/v1.43/_ping" {
			t.Fatalf("requests: %+v", reqs)
		}
	})
	t.Run("DOCKER_API_VERSION", func(t *testing.T) {
		t.Setenv("DOCKER_API_VERSION", "1.42")
		c, err := New(WithHost(fd.Host()))
		if err != nil {
			t.Fatal(err)
		}
		fd.Reset()
		ping(t, c)
		reqs := fd.Requests()
		if len(reqs) != 1 || reqs[0].RawPath != "/v1.42/_ping" {
			t.Fatalf("requests: %+v", reqs)
		}
		if c.APIVersion() != "1.42" {
			t.Fatalf("APIVersion %q", c.APIVersion())
		}
	})
	t.Run("malformed pin is rejected", func(t *testing.T) {
		if _, err := New(WithHost(fd.Host()), WithAPIVersion("latest")); err == nil {
			t.Fatal("expected error for non-numeric version")
		}
	})
}
