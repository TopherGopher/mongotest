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

// lookup carries the environment and the probes discovery needs. Tests
// supply their own so resolution never depends on the machine running them.
type lookup struct {
	goos   string
	getenv func(string) string
	// dialable reports whether a daemon is listening on a unix socket path.
	dialable func(path string) bool
	// listening reports whether a daemon is answering on a TCP address.
	listening func(addr string) bool
}

// realLookup probes the machine this process is running on.
func realLookup() lookup {
	return lookup{goos: runtime.GOOS, getenv: os.Getenv, dialable: socketDialable, listening: tcpListening}
}

// resolveHostFromEnv locates the daemon the way the docker CLI does.
func resolveHostFromEnv() (string, error) {
	return resolveHostWith(realLookup(), configDir(os.Getenv))
}

// resolveHost is resolveHostWith against the real machine, for callers that
// only need to vary the environment.
func resolveHost(getenv func(string) string, configDir string) (string, error) {
	l := realLookup()
	l.getenv = getenv
	return resolveHostWith(l, configDir)
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

// resolveHostWith applies the discovery order: DOCKER_HOST, then
// DOCKER_CONTEXT or currentContext from config.json (resolved through the
// contexts store), then the platform default.
//
// The socket probe is part of that last step only. Configuration is the user
// telling us where the daemon is, so a socket that happens to exist locally
// must never overrule it.
func resolveHostWith(l lookup, configDir string) (string, error) {
	if h := l.getenv("DOCKER_HOST"); h != "" {
		return h, nil
	}
	name := l.getenv("DOCKER_CONTEXT")
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
	return defaultHostFor(l.goos, l.getenv, l.dialable, l.listening)
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

// defaultHostFor returns the platform default when nothing is configured.
//
// On Windows the daemon's default endpoint is a named pipe this client
// cannot dial, so it probes Docker Desktop's optional TCP endpoint and uses
// it when something answers; otherwise it explains how to enable it or which
// variable to set.
//
// Everywhere else it looks for a daemon on the well-known socket paths, so
// that a machine running only Podman works with no configuration at all.
// When none of them answers it returns the Docker default anyway rather than
// an error, so the failure surfaces at dial time with a message that names
// the socket and says what to do about it.
func defaultHostFor(goos string, getenv func(string) string, dialable func(path string) bool, listening func(addr string) bool) (string, error) {
	if goos == "windows" {
		if listening != nil && listening(dockerDesktopTCP) {
			return "tcp://" + dockerDesktopTCP, nil
		}
		return "", ErrNoWindowsEndpoint
	}
	if dialable != nil {
		for _, path := range socketCandidates(goos, getenv) {
			if dialable(path) {
				return "unix://" + path, nil
			}
		}
	}
	return defaultUnixSocket, nil
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
