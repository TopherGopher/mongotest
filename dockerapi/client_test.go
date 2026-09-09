package dockerapi

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json/v2"
	"encoding/pem"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tophergopher/mongotest/dockermock"
)

func TestNewRequiresValidHost(t *testing.T) {
	_, err := New(WithHost("bogus"))
	require.ErrorIs(t, err, ErrInvalidArgument, "a host without a scheme must be rejected as an invalid argument")
	assert.Contains(t, err.Error(), "use unix:///var/run/docker.sock", "the error must tell the caller which host forms are accepted")

	_, err = New(WithHost("npipe:////./pipe/docker_engine"))
	require.ErrorIs(t, err, ErrInvalidArgument, "npipe hosts must be rejected as an invalid argument")
	assert.Contains(t, err.Error(), "DOCKER_HOST=tcp://localhost:2375", "the npipe error must say how to switch Docker Desktop to TCP")
}

func TestNewUsesDiscoveryWhenNoHostOption(t *testing.T) {
	fd := newDaemon(t)
	t.Setenv("DOCKER_HOST", fd.Host())
	t.Setenv("DOCKER_CONTEXT", "")
	c, err := FromEnv()
	require.NoError(t, err, "FromEnv must succeed when DOCKER_HOST points at a listening socket")
	assert.Equal(t, fd.Host(), c.Host(), "the client must report the DOCKER_HOST it discovered")
}

// createBody is what the fake create handler saw.
type createBody struct {
	Image string `json:"Image"`
}

func roundTrip(t *testing.T, fd *dockermock.Daemon, opts ...Option) {
	t.Helper()
	fd.ServeVersion("1.54", "1.40")
	fd.Handle("POST", "/containers/create", func(w http.ResponseWriter, r *http.Request) {
		if r.Host != DummyHost && !strings.HasPrefix(fd.Host(), "tcp://") {
			dockermock.Error(w, 400, "unexpected Host header "+r.Host)
			return
		}
		dockermock.JSON(w, 201, createResponse{ID: "abc123", Warnings: []string{}})
	})
	c, err := New(append([]Option{WithHost(fd.Host()), WithUserAgent("ua-test/1")}, opts...)...)
	require.NoError(t, err, "constructing a client against the fake daemon must succeed")

	resp, err := c.do(context.Background(), http.MethodPost, "/containers/create",
		url.Values{"name": {"mongotest 1"}}, createBody{Image: "mongo:8"})
	require.NoError(t, err, "the round trip to the fake daemon must succeed")
	defer resp.Body.Close()
	require.Equal(t, 201, resp.StatusCode, "the fake answers create with 201")

	var out createResponse
	require.NoError(t, json.UnmarshalRead(resp.Body, &out), "the create response must decode")
	assert.Equal(t, "abc123", out.ID, "the id from the fake must round-trip")

	reqs := fd.Requests()
	require.Len(t, reqs, 2, "exactly two requests are expected: /version negotiation, then the create")
	assert.Equal(t, "/version", reqs[0].RawPath, "the first request must be the unversioned negotiation call")
	r := reqs[1]
	assert.Equal(t, "POST", r.Method, "create is a POST")
	assert.Equal(t, "/containers/create", r.Path, "path without the version prefix")
	assert.Equal(t, "/v1.44/containers/create", r.RawPath, "the negotiated version must prefix the path")
	assert.Equal(t, "mongotest 1", r.Query.Get("name"), "query values must be URL-encoded and decoded intact")
	assert.Equal(t, "application/json", r.Header.Get("Content-Type"), "JSON bodies must be labelled")
	assert.Equal(t, "ua-test/1", r.Header.Get("User-Agent"), "WithUserAgent must be honoured")
	var sent createBody
	require.NoError(t, json.Unmarshal(r.Body, &sent), "the request body must be JSON")
	assert.Equal(t, "mongo:8", sent.Image, "the body must carry the encoded struct")
}

func TestRoundTripUnixSocket(t *testing.T) {
	fd := newDaemon(t)
	roundTrip(t, fd)
	got := fd.Requests()[1].Header.Get("Host")
	assert.True(t, got == "" || got == DummyHost, "unix socket requests must use the placeholder host, got %q", got)
}

func TestRoundTripTCP(t *testing.T) {
	roundTrip(t, newDaemon(t, dockermock.OverTCP()))
}

func TestDoWithoutBodyHasNoContentType(t *testing.T) {
	fd := newDaemon(t)
	fd.ServeVersion("1.44", "")
	c, err := New(WithHost(fd.Host()))
	require.NoError(t, err, "client construction")
	resp, err := c.do(context.Background(), http.MethodGet, "/version", nil, nil)
	require.NoError(t, err, "GET /version through do must succeed")
	resp.Body.Close()
	first := fd.Requests()[0]
	assert.Empty(t, first.Header.Get("Content-Type"), "a body-less request must not claim a content type")
	assert.True(t, strings.HasPrefix(first.Header.Get("User-Agent"), "mongotest"), "the default user agent must identify mongotest, got %q", first.Header.Get("User-Agent"))
}

func TestWithHTTPClientIsUsedAsIs(t *testing.T) {
	fd := newDaemon(t, dockermock.OverTCP())
	fd.Handle("GET", "/_ping", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(200) })
	fd.ServeVersion("1.54", "1.40")
	var seen bool
	hc := &http.Client{Transport: mockRoundTripper(func(r *http.Request) (*http.Response, error) {
		seen = true
		return http.DefaultTransport.RoundTrip(r)
	})}
	c, err := New(WithHost(fd.Host()), WithHTTPClient(hc))
	require.NoError(t, err, "client construction with a custom http.Client")
	resp, err := c.do(context.Background(), http.MethodGet, "/_ping", nil, nil)
	require.NoError(t, err, "ping through the custom client")
	resp.Body.Close()
	assert.True(t, seen, "WithHTTPClient must be the transport actually used for requests")
}

func TestWithLoggerReceivesRequestRecords(t *testing.T) {
	fd := newDaemon(t)
	fd.ServeVersion("1.54", "1.40")
	fd.Handle("GET", "/_ping", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(200) })
	rec := &recordLogger{}
	c, err := New(WithHost(fd.Host()), WithLogger(rec))
	require.NoError(t, err, "client construction with a logger")
	resp, err := c.do(context.Background(), http.MethodGet, "/_ping", nil, nil)
	require.NoError(t, err, "ping")
	resp.Body.Close()
	require.GreaterOrEqual(t, len(rec.entries), 2, "one debug record per request is expected (version, ping)")
	last := rec.entries[len(rec.entries)-1]
	assert.Equal(t, "docker request", last.msg, "request records use a fixed message so log filters can match it")
	assert.Equal(t, "/v1.44/_ping", last.kv["path"], "the record must carry the versioned path")
	assert.Equal(t, 200, last.kv["status"], "the record must carry the status code")
}

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
	require.NoError(t, os.WriteFile(filepath.Join(certDir, "ca.pem"), certPEM, 0o644), "writing ca.pem fixture")
	addr := strings.TrimPrefix(srv.URL, "https://")

	t.Run("tcp host with DOCKER_TLS_VERIFY and ca.pem only", func(t *testing.T) {
		t.Setenv("DOCKER_TLS_VERIFY", "1")
		t.Setenv("DOCKER_CERT_PATH", certDir)
		c, err := New(WithHost("tcp://" + addr))
		require.NoError(t, err, "client construction with TLS from the environment")
		resp, err := c.do(context.Background(), http.MethodGet, "/version", nil, nil)
		require.NoError(t, err, "a TLS round trip trusting ca.pem must succeed")
		defer resp.Body.Close()
		assert.Equal(t, 200, resp.StatusCode, "the TLS server answers 200 when the handshake completed")
	})

	t.Run("client certificate pair is loaded when present", func(t *testing.T) {
		key := srv.TLS.Certificates[0]
		keyDER, err := x509.MarshalPKCS8PrivateKey(key.PrivateKey)
		require.NoError(t, err, "marshalling the fixture key")
		dir := t.TempDir()
		require.NoError(t, os.WriteFile(filepath.Join(dir, "ca.pem"), certPEM, 0o644), "ca.pem fixture")
		require.NoError(t, os.WriteFile(filepath.Join(dir, "cert.pem"), certPEM, 0o644), "cert.pem fixture")
		require.NoError(t, os.WriteFile(filepath.Join(dir, "key.pem"), pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER}), 0o600), "key.pem fixture")
		cfg, err := tlsConfigFromEnv(env(map[string]string{"DOCKER_TLS_VERIFY": "1", "DOCKER_CERT_PATH": dir}))
		require.NoError(t, err, "a complete cert directory must load")
		require.NotNil(t, cfg, "a tls.Config is expected when DOCKER_TLS_VERIFY is set")
		assert.Len(t, cfg.Certificates, 1, "cert.pem and key.pem must be loaded as one client certificate")
		assert.NotNil(t, cfg.RootCAs, "ca.pem must populate the root pool")
	})

	t.Run("no DOCKER_TLS_VERIFY means no tls", func(t *testing.T) {
		cfg, err := tlsConfigFromEnv(env(map[string]string{"DOCKER_CERT_PATH": certDir}))
		require.NoError(t, err, "an unset DOCKER_TLS_VERIFY is not an error")
		assert.Nil(t, cfg, "without DOCKER_TLS_VERIFY no tls.Config may be built even if certificates exist")
	})

	t.Run("missing ca.pem is an error naming the path", func(t *testing.T) {
		empty := t.TempDir()
		_, err := tlsConfigFromEnv(env(map[string]string{"DOCKER_TLS_VERIFY": "1", "DOCKER_CERT_PATH": empty}))
		require.ErrorIs(t, err, ErrInvalidArgument, "a missing ca.pem is a configuration problem")
		assert.Contains(t, err.Error(), filepath.Join(empty, "ca.pem"), "the error must name the file it looked for")
		assert.Contains(t, err.Error(), "DOCKER_CERT_PATH", "the error must name the variable to fix")
	})

	t.Run("WithTLSConfig overrides the environment", func(t *testing.T) {
		t.Setenv("DOCKER_TLS_VERIFY", "1")
		t.Setenv("DOCKER_CERT_PATH", t.TempDir()) // would fail if consulted
		pool := x509.NewCertPool()
		pool.AddCert(srv.Certificate())
		c, err := New(WithHost("tcp://"+addr), WithTLSConfig(&tls.Config{RootCAs: pool}))
		require.NoError(t, err, "an explicit tls.Config must win over an unusable DOCKER_CERT_PATH")
		resp, err := c.do(context.Background(), http.MethodGet, "/version", nil, nil)
		require.NoError(t, err, "round trip with the explicit config")
		resp.Body.Close()
	})

	t.Run("https scheme implies tls with system roots when nothing else is set", func(t *testing.T) {
		t.Setenv("DOCKER_TLS_VERIFY", "")
		c, err := New(WithHost("https://" + addr))
		require.NoError(t, err, "https hosts construct without extra configuration")
		assert.True(t, strings.HasPrefix(c.baseURL, "https://"), "https:// must select a TLS base URL, got %q", c.baseURL)
	})
}
