package dockerapi

import (
	"context"
	"crypto/tls"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// DummyHost is the Host header used for requests over a unix socket. The
// daemon ignores it, but net/http requires a non-empty host in the URL.
const DummyHost = "api.moby.localhost"

const (
	defaultTCPPort    = "2375"
	defaultTCPTLSPort = "2376"
	defaultUnixSocket = "unix:///var/run/docker.sock"
)

// endpoint is the parsed form of a DOCKER_HOST string. It answers two
// questions the rest of the client needs: how to open a connection (which
// network and which address to dial) and which URL prefix to put in front
// of request paths. A unix endpoint dials the socket path and uses the
// placeholder http://api.moby.localhost prefix; a tcp endpoint dials
// host:port and uses http:// or https:// with that address. It is built once
// by parseHost and stored on the Client.
type endpoint struct {
	// scheme is the network to dial: "unix" or "tcp".
	scheme string
	// addr is the socket path for unix, or host:port for tcp (the default
	// port 2375, or 2376 for https, is filled in when missing).
	addr string
	// tls is true when the scheme itself demands TLS (https://). TLS can
	// also be switched on later by DOCKER_TLS_VERIFY or WithTLSConfig.
	tls bool
}

const hostFix = "use unix:///var/run/docker.sock, tcp://host:2375, http://host:2375 or https://host:2376"

// parseHost parses a DOCKER_HOST style string.
func parseHost(host string) (endpoint, error) {
	if strings.TrimSpace(host) == "" {
		return endpoint{}, ErrEmptyHost
	}
	if !strings.Contains(host, "://") {
		return endpoint{}, invalidArg("docker host", host, "it has no scheme", hostFix)
	}
	u, err := url.Parse(host)
	if err != nil {
		return endpoint{}, invalidArg("docker host", host, "it does not parse as a URL ("+err.Error()+")", hostFix)
	}
	switch strings.ToLower(u.Scheme) {
	case "unix":
		// url.Parse puts a path like unix:///var/run/docker.sock into u.Path;
		// unix://relative would land in u.Host, which docker does not accept.
		p := u.Path
		if p == "" && u.Host != "" {
			p = u.Host
		}
		if p == "" {
			return endpoint{}, invalidArg("docker host", host, "it has no socket path", hostFix)
		}
		return endpoint{scheme: "unix", addr: p}, nil
	case "tcp", "http", "https":
		if u.Host == "" {
			return endpoint{}, invalidArg("docker host", host, "it has no address", hostFix)
		}
		useTLS := strings.EqualFold(u.Scheme, "https")
		addr := u.Host
		if _, _, err := net.SplitHostPort(addr); err != nil {
			port := defaultTCPPort
			if useTLS {
				port = defaultTCPTLSPort
			}
			addr = net.JoinHostPort(strings.Trim(addr, "[]"), port)
		}
		return endpoint{scheme: "tcp", addr: addr, tls: useTLS}, nil
	case "npipe":
		return endpoint{}, invalidArg("docker host", host, "Windows named pipes are not supported by this client",
			`enable "Expose daemon on tcp://localhost:2375 without TLS" in Docker Desktop settings and set DOCKER_HOST=tcp://localhost:2375`)
	default:
		return endpoint{}, invalidArg("docker host", host, "the scheme "+u.Scheme+":// is not supported", hostFix)
	}
}

// baseURL is the URL prefix used for plain HTTP requests to this endpoint.
func (ep endpoint) baseURL(useTLS bool) string {
	if ep.scheme == "unix" {
		return "http://" + DummyHost
	}
	if useTLS {
		return "https://" + ep.addr
	}
	return "http://" + ep.addr
}

// newTransport builds an http.Transport that reaches the endpoint. tlsCfg is
// only used for tcp endpoints.
func newTransport(ep endpoint, tlsCfg *tls.Config) *http.Transport {
	tr := &http.Transport{
		Proxy:              nil, // never send daemon traffic through an HTTP proxy
		MaxIdleConns:       8,
		DisableCompression: true,
	}
	switch ep.scheme {
	case "unix":
		sock := ep.addr
		tr.DialContext = func(ctx context.Context, _, _ string) (net.Conn, error) {
			var d net.Dialer
			return d.DialContext(ctx, "unix", sock)
		}
	default:
		tr.DialContext = (&net.Dialer{}).DialContext
		tr.TLSClientConfig = tlsCfg
	}
	return tr
}

// dial opens a raw connection to the endpoint, wrapping it in TLS when
// configured. Used for hijacked requests (exec attach) that cannot go
// through http.Client.
func (c *Client) dial(ctx context.Context) (net.Conn, error) {
	var d net.Dialer
	if c.endpoint.scheme == "unix" {
		conn, err := d.DialContext(ctx, "unix", c.endpoint.addr)
		if err != nil {
			return nil, wrapConnError(c.host, err)
		}
		return conn, nil
	}
	conn, err := d.DialContext(ctx, "tcp", c.endpoint.addr)
	if err != nil {
		return nil, wrapConnError(c.host, err)
	}
	if tcpConn, ok := conn.(*net.TCPConn); ok {
		// Long-running commands with no output must not trip idle timeouts
		// in intermediate network gear.
		_ = tcpConn.SetKeepAlive(true)
		_ = tcpConn.SetKeepAlivePeriod(30 * time.Second)
	}
	if c.tlsConfig == nil {
		return conn, nil
	}
	cfg := c.tlsConfig.Clone()
	if cfg.ServerName == "" {
		host, _, _ := net.SplitHostPort(c.endpoint.addr)
		cfg.ServerName = host
	}
	tc := tls.Client(conn, cfg)
	if err := tc.HandshakeContext(ctx); err != nil {
		_ = conn.Close()
		return nil, wrapConnError(c.host, err)
	}
	return tc, nil
}
