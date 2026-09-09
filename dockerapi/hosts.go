package dockerapi

import (
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"encoding/json/v2"
	"errors"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"time"
)

// dockerConfigFile is the part of ~/.docker/config.json we read.
type dockerConfigFile struct {
	CurrentContext string `json:"currentContext"`
}

// contextMeta is the part of a docker context's meta.json we read.
type contextMeta struct {
	Endpoints map[string]contextEndpoint `json:"Endpoints"`
}

// contextEndpoint is one endpoint inside a docker context.
type contextEndpoint struct {
	Host string `json:"Host"`
}

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

// dockerDesktopTCP is the optional TCP endpoint Docker Desktop for Windows
// exposes when "Expose daemon on tcp://localhost:2375 without TLS" is on.
const dockerDesktopTCP = "localhost:2375"

// ErrNoWindowsEndpoint is returned on Windows when neither DOCKER_HOST nor a
// docker context is set and Docker Desktop's optional TCP endpoint is not
// listening.
var ErrNoWindowsEndpoint = &ConnectionError{
	Host:    "tcp://" + dockerDesktopTCP,
	Problem: "no DOCKER_HOST is set and nothing is listening on Docker Desktop's optional TCP endpoint; Windows named pipes (npipe://) are not supported by this client",
	Fix: `Either enable "Expose daemon on tcp://localhost:2375 without TLS" in Docker Desktop settings, ` +
		`or set the DOCKER_HOST environment variable to a tcp:// endpoint (for example DOCKER_HOST=tcp://localhost:2375)`,
}

func defaultHost() (string, error) {
	return defaultHostFor(runtime.GOOS, tcpListening)
}

// defaultHostFor returns the platform default. On Windows the daemon's
// default endpoint is a named pipe this client cannot dial, so it probes
// Docker Desktop's optional TCP endpoint and uses it when something answers;
// otherwise it explains how to enable it or which variable to set.
func defaultHostFor(goos string, listening func(addr string) bool) (string, error) {
	if goos != "windows" {
		return defaultUnixSocket, nil
	}
	if listening != nil && listening(dockerDesktopTCP) {
		return "tcp://" + dockerDesktopTCP, nil
	}
	return "", ErrNoWindowsEndpoint
}

// tcpListening reports whether something accepts connections on addr.
func tcpListening(addr string) bool {
	conn, err := net.DialTimeout("tcp", addr, 500*time.Millisecond)
	if err != nil {
		return false
	}
	_ = conn.Close()
	return true
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
		return "", invalidArg("docker config file", path, "it could not be read ("+err.Error()+")", "fix the file permissions, or set DOCKER_HOST to bypass context lookup")
	}
	var cfg dockerConfigFile
	if err := json.Unmarshal(b, &cfg); err != nil {
		return "", invalidArg("docker config file", path, "it is not valid JSON ("+err.Error()+")", "repair the file, or set DOCKER_HOST to bypass context lookup")
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
		return "", invalidArg("docker context", name, "its metadata file "+path+" could not be read ("+err.Error()+")",
			"run `docker context ls` to see the contexts that exist, `docker context use <name>` to switch, or set DOCKER_HOST directly")
	}
	var meta contextMeta
	if err := json.Unmarshal(b, &meta); err != nil {
		return "", invalidArg("docker context", name, "its metadata file "+path+" is not valid JSON ("+err.Error()+")", "recreate the context with `docker context create`, or set DOCKER_HOST directly")
	}
	ep, ok := meta.Endpoints["docker"]
	if !ok || ep.Host == "" {
		return "", invalidArg("docker context", name, "it has no docker endpoint in "+path, "recreate the context with `docker context create --docker host=...`, or set DOCKER_HOST directly")
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
		return nil, invalidArg("DOCKER_CERT_PATH", dir, "DOCKER_TLS_VERIFY is set but "+caPath+" could not be read ("+err.Error()+")",
			"point DOCKER_CERT_PATH at the directory holding the daemon's ca.pem (and cert.pem/key.pem when client certificates are required), or unset DOCKER_TLS_VERIFY")
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(caPEM) {
		return nil, invalidArg("DOCKER_CERT_PATH", dir, "no certificates were found in "+caPath, "make sure ca.pem is a PEM-encoded certificate")
	}
	cfg := &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS12}
	certPath, keyPath := filepath.Join(dir, "cert.pem"), filepath.Join(dir, "key.pem")
	_, certErr := os.Stat(certPath)
	_, keyErr := os.Stat(keyPath)
	if certErr == nil && keyErr == nil {
		pair, err := tls.LoadX509KeyPair(certPath, keyPath)
		if err != nil {
			return nil, invalidArg("DOCKER_CERT_PATH", dir, "cert.pem and key.pem do not form a valid key pair ("+err.Error()+")", "regenerate the client certificate, or remove the pair to connect without one")
		}
		cfg.Certificates = []tls.Certificate{pair}
	}
	return cfg, nil
}
