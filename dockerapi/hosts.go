package dockerapi

import (
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
)

// resolveHostFromEnv locates the daemon the way the docker CLI does.
func resolveHostFromEnv() (string, error) {
	return resolveHost(os.Getenv, configDir(os.Getenv))
}

// configDir returns the Docker config directory: DOCKER_CONFIG, else
// $HOME/.docker.
func configDir(getenv func(string) string) string {
	if d := getenv("DOCKER_CONFIG"); d != "" {
		return d
	}
	home := getenv("HOME")
	if home == "" {
		if h, err := os.UserHomeDir(); err == nil {
			home = h
		}
	}
	return filepath.Join(home, ".docker")
}

// resolveHost applies the discovery order: DOCKER_HOST, then DOCKER_CONTEXT
// or currentContext from config.json (resolved through the contexts store),
// then the platform default.
func resolveHost(getenv func(string) string, configDir string) (string, error) {
	if h := getenv("DOCKER_HOST"); h != "" {
		return h, nil
	}
	name := getenv("DOCKER_CONTEXT")
	if name == "" {
		var err error
		name, err = currentContext(configDir)
		if err != nil {
			return "", err
		}
	}
	if name != "" && name != "default" {
		return hostFromContext(configDir, name)
	}
	return defaultHost()
}

func defaultHost() (string, error) {
	if runtime.GOOS == "windows" {
		return "", errors.New("dockerapi: the default Windows named pipe is not supported; set DOCKER_HOST to a tcp:// endpoint")
	}
	return defaultUnixSocket, nil
}

// currentContext reads currentContext from <configDir>/config.json. A missing
// file means no context is selected.
func currentContext(configDir string) (string, error) {
	path := filepath.Join(configDir, "config.json")
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("dockerapi: read %s: %w", path, err)
	}
	var cfg struct {
		CurrentContext string `json:"currentContext"`
	}
	if err := json.Unmarshal(b, &cfg); err != nil {
		return "", fmt.Errorf("dockerapi: parse %s: %w", path, err)
	}
	return cfg.CurrentContext, nil
}

// hostFromContext resolves a named docker context to its docker endpoint by
// reading <configDir>/contexts/meta/<sha256(name)>/meta.json.
func hostFromContext(configDir, name string) (string, error) {
	sum := sha256.Sum256([]byte(name))
	path := filepath.Join(configDir, "contexts", "meta", hex.EncodeToString(sum[:]), "meta.json")
	b, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("dockerapi: docker context %q: read %s: %w", name, path, err)
	}
	var meta struct {
		Endpoints map[string]struct {
			Host string `json:"Host"`
		} `json:"Endpoints"`
	}
	if err := json.Unmarshal(b, &meta); err != nil {
		return "", fmt.Errorf("dockerapi: docker context %q: parse %s: %w", name, path, err)
	}
	ep, ok := meta.Endpoints["docker"]
	if !ok || ep.Host == "" {
		return "", fmt.Errorf("dockerapi: docker context %q has no docker endpoint in %s", name, path)
	}
	return ep.Host, nil
}

// tlsConfigFromEnv builds a tls.Config from DOCKER_TLS_VERIFY and
// DOCKER_CERT_PATH. It returns nil when DOCKER_TLS_VERIFY is not set.
func tlsConfigFromEnv(getenv func(string) string) (*tls.Config, error) {
	if getenv("DOCKER_TLS_VERIFY") == "" {
		return nil, nil
	}
	dir := getenv("DOCKER_CERT_PATH")
	if dir == "" {
		dir = configDir(getenv)
	}
	caPath := filepath.Join(dir, "ca.pem")
	caPEM, err := os.ReadFile(caPath)
	if err != nil {
		return nil, fmt.Errorf("dockerapi: DOCKER_TLS_VERIFY is set but %s could not be read: %w", caPath, err)
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(caPEM) {
		return nil, fmt.Errorf("dockerapi: no certificates found in %s", caPath)
	}
	cfg := &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS12}
	certPath, keyPath := filepath.Join(dir, "cert.pem"), filepath.Join(dir, "key.pem")
	_, certErr := os.Stat(certPath)
	_, keyErr := os.Stat(keyPath)
	if certErr == nil && keyErr == nil {
		pair, err := tls.LoadX509KeyPair(certPath, keyPath)
		if err != nil {
			return nil, fmt.Errorf("dockerapi: load client certificate %s / %s: %w", certPath, keyPath, err)
		}
		cfg.Certificates = []tls.Certificate{pair}
	}
	return cfg, nil
}
