package dockerapi

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tophergopher/mongotest/internal/fakedaemon"
)

func TestNewRequiresValidHost(t *testing.T) {
	if _, err := New(WithHost("bogus")); err == nil {
		t.Fatal("expected error for host without scheme")
	}
	if _, err := New(WithHost("npipe:////./pipe/docker_engine")); err == nil || !strings.Contains(err.Error(), "npipe") {
		t.Fatalf("npipe should be rejected clearly, got %v", err)
	}
}

func TestNewUsesDiscoveryWhenNoHostOption(t *testing.T) {
	fd := fakedaemon.New(t)
	t.Setenv("DOCKER_HOST", fd.Host())
	t.Setenv("DOCKER_CONTEXT", "")
	c, err := FromEnv()
	if err != nil {
		t.Fatal(err)
	}
	if c.Host() != fd.Host() {
		t.Fatalf("Host() = %q, want %q", c.Host(), fd.Host())
	}
}

func roundTrip(t *testing.T, fd *fakedaemon.Server, opts ...Option) {
	t.Helper()
	fd.Handle("POST", "/containers/create", func(w http.ResponseWriter, r *http.Request) {
		if r.Host != DummyHost && !strings.HasPrefix(fd.Host(), "tcp://") {
			fakedaemon.Error(w, 400, "unexpected Host header "+r.Host)
			return
		}
		fakedaemon.JSON(w, 201, map[string]any{"Id": "abc123", "Warnings": []string{}})
	})
	c, err := New(append([]Option{WithHost(fd.Host()), WithUserAgent("ua-test/1")}, opts...)...)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := c.do(context.Background(), http.MethodPost, "/containers/create",
		url.Values{"name": {"mongotest 1"}}, map[string]any{"Image": "mongo:8"})
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 201 {
		b, _ := io.ReadAll(resp.Body)
		t.Fatalf("status %d: %s", resp.StatusCode, b)
	}
	var out struct{ Id string }
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil || out.Id != "abc123" {
		t.Fatalf("decode: %v %+v", err, out)
	}
	reqs := fd.Requests()
	if len(reqs) != 1 {
		t.Fatalf("want 1 request, got %d", len(reqs))
	}
	r := reqs[0]
	if r.Method != "POST" || r.Path != "/containers/create" {
		t.Fatalf("method/path: %s %s", r.Method, r.Path)
	}
	if r.Query.Get("name") != "mongotest 1" {
		t.Fatalf("query: %v", r.Query)
	}
	if ct := r.Header.Get("Content-Type"); ct != "application/json" {
		t.Fatalf("content-type: %q", ct)
	}
	if ua := r.Header.Get("User-Agent"); ua != "ua-test/1" {
		t.Fatalf("user-agent: %q", ua)
	}
	if strings.TrimSpace(string(r.Body)) != `{"Image":"mongo:8"}` {
		t.Fatalf("body: %q", r.Body)
	}
}

func TestRoundTripUnixSocket(t *testing.T) {
	fd := fakedaemon.New(t)
	roundTrip(t, fd)
	if got := fd.Requests()[0].Header.Get("Host"); got != "" && got != DummyHost {
		t.Fatalf("Host header %q", got)
	}
}

func TestRoundTripTCP(t *testing.T) {
	roundTrip(t, fakedaemon.NewTCP(t))
}

func TestDoWithoutBodyHasNoContentType(t *testing.T) {
	fd := fakedaemon.New(t)
	fd.Handle("GET", "/version", func(w http.ResponseWriter, _ *http.Request) {
		fakedaemon.JSON(w, 200, map[string]string{"ApiVersion": "1.44"})
	})
	c, err := New(WithHost(fd.Host()))
	if err != nil {
		t.Fatal(err)
	}
	resp, err := c.do(context.Background(), http.MethodGet, "/version", nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if ct := fd.Requests()[0].Header.Get("Content-Type"); ct != "" {
		t.Fatalf("unexpected content-type %q", ct)
	}
	if ua := fd.Requests()[0].Header.Get("User-Agent"); !strings.HasPrefix(ua, "mongotest") {
		t.Fatalf("default user-agent %q", ua)
	}
}

func TestWithHTTPClientIsUsedAsIs(t *testing.T) {
	fd := fakedaemon.NewTCP(t)
	fd.Handle("GET", "/_ping", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(200) })
	var seen bool
	hc := &http.Client{Transport: roundTripperFunc(func(r *http.Request) (*http.Response, error) {
		seen = true
		return http.DefaultTransport.RoundTrip(r)
	})}
	c, err := New(WithHost(fd.Host()), WithHTTPClient(hc))
	if err != nil {
		t.Fatal(err)
	}
	resp, err := c.do(context.Background(), http.MethodGet, "/_ping", nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if !seen {
		t.Fatal("custom http.Client was not used")
	}
}

type roundTripperFunc func(*http.Request) (*http.Response, error)

func (f roundTripperFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

// TLS: a DOCKER_CERT_PATH directory built from an httptest TLS server.
func TestTLSFromEnv(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.TLS == nil {
			w.WriteHeader(400)
			return
		}
		_, _ = io.WriteString(w, `{"ApiVersion":"1.44"}`)
	}))
	defer srv.Close()

	certDir := t.TempDir()
	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: srv.Certificate().Raw})
	if err := os.WriteFile(filepath.Join(certDir, "ca.pem"), certPEM, 0o644); err != nil {
		t.Fatal(err)
	}
	addr := strings.TrimPrefix(srv.URL, "https://")

	t.Run("tcp host with DOCKER_TLS_VERIFY and ca.pem only", func(t *testing.T) {
		t.Setenv("DOCKER_TLS_VERIFY", "1")
		t.Setenv("DOCKER_CERT_PATH", certDir)
		c, err := New(WithHost("tcp://" + addr))
		if err != nil {
			t.Fatal(err)
		}
		resp, err := c.do(context.Background(), http.MethodGet, "/version", nil, nil)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		if resp.StatusCode != 200 {
			t.Fatalf("status %d", resp.StatusCode)
		}
	})

	t.Run("client certificate pair is loaded when present", func(t *testing.T) {
		// Reuse the server's own key pair as a client certificate; only the
		// presence of the pair in the tls.Config is asserted.
		key := srv.TLS.Certificates[0]
		keyDER, err := x509.MarshalPKCS8PrivateKey(key.PrivateKey)
		if err != nil {
			t.Fatal(err)
		}
		dir := t.TempDir()
		_ = os.WriteFile(filepath.Join(dir, "ca.pem"), certPEM, 0o644)
		_ = os.WriteFile(filepath.Join(dir, "cert.pem"), certPEM, 0o644)
		_ = os.WriteFile(filepath.Join(dir, "key.pem"), pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER}), 0o600)
		cfg, err := tlsConfigFromEnv(env(map[string]string{"DOCKER_TLS_VERIFY": "1", "DOCKER_CERT_PATH": dir}))
		if err != nil {
			t.Fatal(err)
		}
		if cfg == nil || len(cfg.Certificates) != 1 || cfg.RootCAs == nil {
			t.Fatalf("tls config: %+v", cfg)
		}
	})

	t.Run("no DOCKER_TLS_VERIFY means no tls", func(t *testing.T) {
		cfg, err := tlsConfigFromEnv(env(map[string]string{"DOCKER_CERT_PATH": certDir}))
		if err != nil || cfg != nil {
			t.Fatalf("cfg=%v err=%v", cfg, err)
		}
	})

	t.Run("missing ca.pem is an error naming the path", func(t *testing.T) {
		empty := t.TempDir()
		_, err := tlsConfigFromEnv(env(map[string]string{"DOCKER_TLS_VERIFY": "1", "DOCKER_CERT_PATH": empty}))
		if err == nil || !strings.Contains(err.Error(), filepath.Join(empty, "ca.pem")) {
			t.Fatalf("err = %v", err)
		}
	})

	t.Run("WithTLSConfig overrides the environment", func(t *testing.T) {
		t.Setenv("DOCKER_TLS_VERIFY", "1")
		t.Setenv("DOCKER_CERT_PATH", t.TempDir()) // would fail if consulted
		pool := x509.NewCertPool()
		pool.AddCert(srv.Certificate())
		c, err := New(WithHost("tcp://"+addr), WithTLSConfig(&tls.Config{RootCAs: pool}))
		if err != nil {
			t.Fatal(err)
		}
		resp, err := c.do(context.Background(), http.MethodGet, "/version", nil, nil)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
	})

	t.Run("https scheme implies tls with system roots when nothing else is set", func(t *testing.T) {
		t.Setenv("DOCKER_TLS_VERIFY", "")
		c, err := New(WithHost("https://" + addr))
		if err != nil {
			t.Fatal(err)
		}
		if !strings.HasPrefix(c.baseURL, "https://") {
			t.Fatalf("baseURL %q", c.baseURL)
		}
	})
}
