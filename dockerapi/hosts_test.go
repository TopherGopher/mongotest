package dockerapi

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestParseHost(t *testing.T) {
	tests := []struct {
		in         string
		scheme     string
		addr       string
		tls        bool
		wantErrSub string
	}{
		{in: "unix:///var/run/docker.sock", scheme: "unix", addr: "/var/run/docker.sock"},
		{in: "unix://" + "/tmp/x.sock", scheme: "unix", addr: "/tmp/x.sock"},
		{in: "tcp://127.0.0.1:2375", scheme: "tcp", addr: "127.0.0.1:2375"},
		{in: "tcp://docker.example.com", scheme: "tcp", addr: "docker.example.com:2375"},
		{in: "http://10.0.0.5:2375", scheme: "tcp", addr: "10.0.0.5:2375"},
		{in: "https://10.0.0.5", scheme: "tcp", addr: "10.0.0.5:2376", tls: true},
		{in: "npipe:////./pipe/docker_engine", wantErrSub: "npipe"},
		{in: "ssh://user@host", wantErrSub: "unsupported"},
		{in: "unix://", wantErrSub: "path"},
		{in: "", wantErrSub: "empty"},
		{in: "just-a-hostname", wantErrSub: "scheme"},
	}
	for _, tc := range tests {
		ep, err := parseHost(tc.in)
		if tc.wantErrSub != "" {
			if err == nil || !strings.Contains(err.Error(), tc.wantErrSub) {
				t.Errorf("parseHost(%q): err = %v, want containing %q", tc.in, err, tc.wantErrSub)
			}
			continue
		}
		if err != nil {
			t.Errorf("parseHost(%q): %v", tc.in, err)
			continue
		}
		if ep.scheme != tc.scheme || ep.addr != tc.addr || ep.tls != tc.tls {
			t.Errorf("parseHost(%q) = %+v, want scheme=%s addr=%s tls=%v", tc.in, ep, tc.scheme, tc.addr, tc.tls)
		}
	}
}

// env returns a getenv function backed by a map.
func env(m map[string]string) func(string) string {
	return func(k string) string { return m[k] }
}

func writeContext(t *testing.T, configDir, name, host string) {
	t.Helper()
	sum := sha256.Sum256([]byte(name))
	dir := filepath.Join(configDir, "contexts", "meta", hex.EncodeToString(sum[:]))
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	meta := `{"Name":"` + name + `","Metadata":{},"Endpoints":{"docker":{"Host":"` + host + `","SkipTLSVerify":false}}}`
	if err := os.WriteFile(filepath.Join(dir, "meta.json"), []byte(meta), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestResolveHostPrecedence(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("default host is a named pipe on windows")
	}
	cfg := t.TempDir()
	if err := os.WriteFile(filepath.Join(cfg, "config.json"), []byte(`{"currentContext":"colima"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	writeContext(t, cfg, "colima", "unix:///home/me/.colima/default/docker.sock")
	writeContext(t, cfg, "remote", "tcp://10.1.2.3:2376")

	t.Run("DOCKER_HOST wins over everything", func(t *testing.T) {
		got, err := resolveHost(env(map[string]string{"DOCKER_HOST": "tcp://1.2.3.4:2375", "DOCKER_CONTEXT": "remote"}), cfg)
		if err != nil || got != "tcp://1.2.3.4:2375" {
			t.Fatalf("got %q, %v", got, err)
		}
	})
	t.Run("DOCKER_CONTEXT wins over currentContext", func(t *testing.T) {
		got, err := resolveHost(env(map[string]string{"DOCKER_CONTEXT": "remote"}), cfg)
		if err != nil || got != "tcp://10.1.2.3:2376" {
			t.Fatalf("got %q, %v", got, err)
		}
	})
	t.Run("currentContext from config.json", func(t *testing.T) {
		got, err := resolveHost(env(nil), cfg)
		if err != nil || got != "unix:///home/me/.colima/default/docker.sock" {
			t.Fatalf("got %q, %v", got, err)
		}
	})
	t.Run("context named default means the default socket", func(t *testing.T) {
		got, err := resolveHost(env(map[string]string{"DOCKER_CONTEXT": "default"}), cfg)
		if err != nil || got != "unix:///var/run/docker.sock" {
			t.Fatalf("got %q, %v", got, err)
		}
	})
	t.Run("missing context metadata is a descriptive error", func(t *testing.T) {
		_, err := resolveHost(env(map[string]string{"DOCKER_CONTEXT": "ghost"}), cfg)
		if err == nil || !strings.Contains(err.Error(), "ghost") || !strings.Contains(err.Error(), "meta.json") {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("nothing set uses the default socket", func(t *testing.T) {
		got, err := resolveHost(env(nil), t.TempDir())
		if err != nil || got != "unix:///var/run/docker.sock" {
			t.Fatalf("got %q, %v", got, err)
		}
	})
	t.Run("unreadable config.json is a descriptive error", func(t *testing.T) {
		bad := t.TempDir()
		_ = os.WriteFile(filepath.Join(bad, "config.json"), []byte(`{not json`), 0o644)
		_, err := resolveHost(env(nil), bad)
		if err == nil || !strings.Contains(err.Error(), "config.json") {
			t.Fatalf("err = %v", err)
		}
	})
}

func TestConfigDir(t *testing.T) {
	if got := configDir(env(map[string]string{"DOCKER_CONFIG": "/etc/dockercfg"})); got != "/etc/dockercfg" {
		t.Fatalf("DOCKER_CONFIG not honoured: %q", got)
	}
	if got := configDir(env(map[string]string{"HOME": "/home/me"})); got != filepath.Join("/home/me", ".docker") {
		t.Fatalf("HOME fallback: %q", got)
	}
}

func TestDefaultHostWindows(t *testing.T) {
	listening := func(addr string) bool { return addr == "localhost:2375" }
	got, err := defaultHostFor("windows", listening)
	if err != nil || got != "tcp://localhost:2375" {
		t.Fatalf("Docker Desktop TCP endpoint should be used when it answers: %q %v", got, err)
	}
	_, err = defaultHostFor("windows", func(string) bool { return false })
	if err == nil {
		t.Fatal("expected an error when nothing listens on the Docker Desktop TCP endpoint")
	}
	for _, want := range []string{"DOCKER_HOST", "tcp://localhost:2375", "Expose daemon", "npipe"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("windows error %q should mention %q", err, want)
		}
	}
	got, err = defaultHostFor("linux", func(string) bool { t.Error("probe must not run on linux"); return false })
	if err != nil || got != "unix:///var/run/docker.sock" {
		t.Fatalf("linux default: %q %v", got, err)
	}
	got, err = defaultHostFor("darwin", nil)
	if err != nil || got != "unix:///var/run/docker.sock" {
		t.Fatalf("darwin default: %q %v", got, err)
	}
}
