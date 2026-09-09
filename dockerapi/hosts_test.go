package dockerapi

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestParseHost(t *testing.T) {
	good := []struct {
		in     string
		scheme string
		addr   string
		tls    bool
	}{
		{in: "unix:///var/run/docker.sock", scheme: "unix", addr: "/var/run/docker.sock"},
		{in: "unix:///tmp/x.sock", scheme: "unix", addr: "/tmp/x.sock"},
		{in: "tcp://127.0.0.1:2375", scheme: "tcp", addr: "127.0.0.1:2375"},
		{in: "tcp://docker.example.com", scheme: "tcp", addr: "docker.example.com:2375"},
		{in: "http://10.0.0.5:2375", scheme: "tcp", addr: "10.0.0.5:2375"},
		{in: "https://10.0.0.5", scheme: "tcp", addr: "10.0.0.5:2376", tls: true},
	}
	for _, tc := range good {
		ep, err := parseHost(tc.in)
		require.NoError(t, err, "%q is a valid docker host", tc.in)
		assert.Equal(t, tc.scheme, ep.scheme, "%q: dial network", tc.in)
		assert.Equal(t, tc.addr, ep.addr, "%q: dial address (default ports filled in)", tc.in)
		assert.Equal(t, tc.tls, ep.tls, "%q: only https:// implies TLS", tc.in)
	}
	bad := []struct{ in, wantSub string }{
		{"npipe:////./pipe/docker_engine", "Docker Desktop"},
		{"ssh://user@host", "not supported"},
		{"unix://", "socket path"},
		{"", "empty"},
		{"just-a-hostname", "no scheme"},
	}
	for _, tc := range bad {
		_, err := parseHost(tc.in)
		require.ErrorIs(t, err, ErrInvalidArgument, "%q must be rejected as an invalid argument", tc.in)
		assert.Contains(t, err.Error(), tc.wantSub, "%q: the error must explain the problem", tc.in)
	}
	_, err := parseHost("")
	assert.Same(t, ErrEmptyHost, err, "an empty host returns the predeclared ErrEmptyHost so callers can compare it")
}

// env returns a getenv function backed by a map.
func env(m map[string]string) func(string) string {
	return func(k string) string { return m[k] }
}

func writeContext(t *testing.T, configDir, name, host string) {
	t.Helper()
	sum := sha256.Sum256([]byte(name))
	dir := filepath.Join(configDir, "contexts", "meta", hex.EncodeToString(sum[:]))
	require.NoError(t, os.MkdirAll(dir, 0o755), "creating the context meta directory fixture")
	meta := `{"Name":"` + name + `","Metadata":{},"Endpoints":{"docker":{"Host":"` + host + `","SkipTLSVerify":false}}}`
	require.NoError(t, os.WriteFile(filepath.Join(dir, "meta.json"), []byte(meta), 0o644), "writing meta.json fixture")
}

func TestResolveHostPrecedence(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("default host is a named pipe on windows")
	}
	cfg := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(cfg, "config.json"), []byte(`{"currentContext":"colima"}`), 0o644), "writing config.json fixture")
	writeContext(t, cfg, "colima", "unix:///home/me/.colima/default/docker.sock")
	writeContext(t, cfg, "remote", "tcp://10.1.2.3:2376")

	t.Run("DOCKER_HOST wins over everything", func(t *testing.T) {
		got, err := resolveHost(env(map[string]string{"DOCKER_HOST": "tcp://1.2.3.4:2375", "DOCKER_CONTEXT": "remote"}), cfg)
		require.NoError(t, err, "resolution with DOCKER_HOST set")
		assert.Equal(t, "tcp://1.2.3.4:2375", got, "DOCKER_HOST must take precedence over DOCKER_CONTEXT and config.json")
	})
	t.Run("DOCKER_CONTEXT wins over currentContext", func(t *testing.T) {
		got, err := resolveHost(env(map[string]string{"DOCKER_CONTEXT": "remote"}), cfg)
		require.NoError(t, err, "resolution with DOCKER_CONTEXT set")
		assert.Equal(t, "tcp://10.1.2.3:2376", got, "DOCKER_CONTEXT must override currentContext from config.json")
	})
	t.Run("currentContext from config.json", func(t *testing.T) {
		got, err := resolveHost(env(nil), cfg)
		require.NoError(t, err, "resolution through config.json")
		assert.Equal(t, "unix:///home/me/.colima/default/docker.sock", got, "currentContext must resolve through contexts/meta")
	})
	t.Run("context named default means the default socket", func(t *testing.T) {
		got, err := resolveHost(env(map[string]string{"DOCKER_CONTEXT": "default"}), cfg)
		require.NoError(t, err, "resolution with the default context")
		assert.Equal(t, "unix:///var/run/docker.sock", got, "the 'default' context has no meta file and means the default socket")
	})
	t.Run("missing context metadata is a descriptive error", func(t *testing.T) {
		_, err := resolveHost(env(map[string]string{"DOCKER_CONTEXT": "ghost"}), cfg)
		require.ErrorIs(t, err, ErrInvalidArgument, "an unknown context is a configuration problem")
		assert.Contains(t, err.Error(), "ghost", "the error must name the context")
		assert.Contains(t, err.Error(), "meta.json", "the error must name the file that was missing")
		assert.Contains(t, err.Error(), "docker context ls", "the error must tell the user how to list contexts")
	})
	t.Run("nothing set uses the default socket", func(t *testing.T) {
		got, err := resolveHost(env(nil), t.TempDir())
		require.NoError(t, err, "resolution with nothing configured")
		assert.Equal(t, "unix:///var/run/docker.sock", got, "the platform default applies when nothing is configured")
	})
	t.Run("unreadable config.json is a descriptive error", func(t *testing.T) {
		bad := t.TempDir()
		require.NoError(t, os.WriteFile(filepath.Join(bad, "config.json"), []byte(`{not json`), 0o644), "writing a broken config.json")
		_, err := resolveHost(env(nil), bad)
		require.ErrorIs(t, err, ErrInvalidArgument, "a broken config.json is a configuration problem")
		assert.Contains(t, err.Error(), "config.json", "the error must name the file")
		assert.Contains(t, err.Error(), "DOCKER_HOST", "the error must offer DOCKER_HOST as the bypass")
	})
}

func TestConfigDir(t *testing.T) {
	assert.Equal(t, "/etc/dockercfg", configDir(env(map[string]string{"DOCKER_CONFIG": "/etc/dockercfg"})), "DOCKER_CONFIG must be honoured")
	assert.Equal(t, filepath.Join("/home/me", ".docker"), configDir(env(map[string]string{"HOME": "/home/me"})), "without DOCKER_CONFIG the directory is $HOME/.docker")
}

func TestDefaultHostWindows(t *testing.T) {
	listening := func(addr string) bool { return addr == "localhost:2375" }
	got, err := defaultHostFor("windows", listening)
	require.NoError(t, err, "Docker Desktop's TCP endpoint answering must be enough on Windows")
	assert.Equal(t, "tcp://localhost:2375", got, "the probed Docker Desktop endpoint must be used")

	_, err = defaultHostFor("windows", func(string) bool { return false })
	require.ErrorIs(t, err, ErrConnectionFailed, "nothing listening on Windows is a connection problem")
	assert.Same(t, ErrNoWindowsEndpoint, err, "the predeclared ErrNoWindowsEndpoint is returned so callers can compare it")
	for _, want := range []string{"DOCKER_HOST", "tcp://localhost:2375", "Expose daemon", "npipe"} {
		assert.Contains(t, err.Error(), want, "the Windows error must mention %q so the user knows what to do", want)
	}

	got, err = defaultHostFor("linux", func(string) bool { t.Error("the TCP probe must not run on linux"); return false })
	require.NoError(t, err, "linux default")
	assert.Equal(t, "unix:///var/run/docker.sock", got, "linux defaults to the unix socket")
	got, err = defaultHostFor("darwin", nil)
	require.NoError(t, err, "darwin default")
	assert.Equal(t, "unix:///var/run/docker.sock", got, "darwin defaults to the unix socket")
}
