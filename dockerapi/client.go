package dockerapi

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json/v2"
	"errors"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
)

const defaultUserAgent = "mongotest-dockerapi/1"

// Client talks to one Docker Engine API endpoint. It is safe for concurrent
// use; API version negotiation happens once, on the first request.
type Client struct {
	host       string
	endpoint   endpoint
	baseURL    string
	httpClient *http.Client
	tlsConfig  *tls.Config
	userAgent  string
	logger     Logger
	versionState
}

// Option configures a Client.
type Option func(*Client) error

// WithHost sets the daemon address (unix:///path, tcp://host:port,
// http://host:port, https://host:port) and skips discovery.
func WithHost(host string) Option {
	return func(c *Client) error {
		c.host = host
		return nil
	}
}

// WithHTTPClient supplies the http.Client used for ordinary requests. The
// caller is responsible for making it reach the daemon (for a unix socket
// that means a Transport with a custom DialContext). Hijacked requests still
// dial the endpoint directly.
func WithHTTPClient(hc *http.Client) Option {
	return func(c *Client) error {
		c.httpClient = hc
		return nil
	}
}

// WithTLSConfig sets the TLS configuration for tcp endpoints, overriding
// anything derived from the environment.
func WithTLSConfig(cfg *tls.Config) Option {
	return func(c *Client) error {
		c.tlsConfig = cfg
		return nil
	}
}

// WithUserAgent sets the User-Agent header sent with every request.
func WithUserAgent(ua string) Option {
	return func(c *Client) error {
		c.userAgent = ua
		return nil
	}
}

// WithLogger sets the logger that receives one Debug record per request.
// *slog.Logger satisfies Logger directly. The default discards everything.
func WithLogger(l Logger) Option {
	return func(c *Client) error {
		if l != nil {
			c.logger = l
		}
		return nil
	}
}

// New creates a Client. When WithHost is not given the daemon is located from
// the environment; see the package documentation for the order.
func New(opts ...Option) (*Client, error) {
	c := &Client{userAgent: defaultUserAgent, logger: NopLogger()}
	for _, opt := range opts {
		if err := opt(c); err != nil {
			return nil, err
		}
	}
	if !c.versionPinned {
		if v := os.Getenv("DOCKER_API_VERSION"); v != "" {
			if err := WithAPIVersion(v)(c); err != nil {
				return nil, err
			}
		}
	}
	if c.host == "" {
		h, err := resolveHostFromEnv()
		if err != nil {
			return nil, err
		}
		c.host = h
	}
	ep, err := parseHost(c.host)
	if err != nil {
		return nil, err
	}
	c.endpoint = ep
	if ep.scheme == "tcp" && c.tlsConfig == nil {
		cfg, err := tlsConfigFromEnv(os.Getenv)
		if err != nil {
			return nil, err
		}
		if cfg == nil && ep.tls {
			cfg = &tls.Config{MinVersion: tls.VersionTLS12}
		}
		c.tlsConfig = cfg
	}
	if ep.scheme == "unix" {
		c.tlsConfig = nil
	}
	c.baseURL = ep.baseURL(c.tlsConfig != nil)
	if c.httpClient == nil {
		c.httpClient = &http.Client{Transport: newTransport(ep, c.tlsConfig)}
	}
	return c, nil
}

// FromEnv is New() with no options: the daemon is discovered from the
// environment exactly like the docker CLI does it.
func FromEnv() (*Client, error) { return New() }

// Host returns the daemon address the client resolved, in DOCKER_HOST form.
func (c *Client) Host() string { return c.host }

// do performs one request against the negotiated API version. path is the
// endpoint path without a version prefix (for example "/containers/create");
// query may be nil; body, when non-nil, is JSON encoded and sent with
// Content-Type application/json. Request bodies are a few hundred bytes of
// JSON at most, so they are encoded in memory to give the daemon a
// Content-Length; large payloads (archives) go through doRaw with a stream.
// The caller must close the response body.
func (c *Client) do(ctx context.Context, method, path string, query url.Values, body any) (*http.Response, error) {
	var rdr io.Reader
	if body != nil {
		buf, err := json.Marshal(body)
		if err != nil {
			return nil, &ResponseError{Method: method, Path: path, Problem: "could not encode the request body", Err: err}
		}
		rdr = bytes.NewReader(buf)
	}
	return c.doRaw(ctx, method, path, query, rdr, body != nil, "application/json")
}

// doRaw is do with a caller-provided body stream and content type.
func (c *Client) doRaw(ctx context.Context, method, path string, query url.Values, body io.Reader, hasBody bool, contentType string) (*http.Response, error) {
	if err := c.Negotiate(ctx); err != nil {
		return nil, err
	}
	return c.request(ctx, method, path, query, body, hasBody, contentType, true)
}

// request builds and sends one HTTP request. When versioned is true the path
// is prefixed with /v<apiVersion>; Negotiate itself passes false.
func (c *Client) request(ctx context.Context, method, path string, query url.Values, body io.Reader, hasBody bool, contentType string, versioned bool) (*http.Response, error) {
	if !strings.HasPrefix(path, "/") {
		path = "/" + path
	}
	if versioned {
		path = "/v" + c.apiVersion + path
	}
	u := c.baseURL + path
	if len(query) > 0 {
		u += "?" + query.Encode()
	}
	req, err := http.NewRequestWithContext(ctx, method, u, body)
	if err != nil {
		return nil, &ResponseError{Method: method, Path: path, Problem: "could not build the request", Err: err}
	}
	req.Header.Set("User-Agent", c.userAgent)
	if hasBody {
		req.Header.Set("Content-Type", contentType)
	}
	start := now()
	resp, err := c.httpClient.Do(req)
	if err != nil {
		c.logger.Debug("docker request failed", "method", method, "path", path, "error", err)
		// A cancellation or deadline is the caller's own doing. It is not a
		// bad response, and it must keep matching context.Canceled and
		// context.DeadlineExceeded, so it is returned untouched rather than
		// described as a daemon that answered badly.
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return nil, err
		}
		if werr := wrapConnError(c.host, err); werr != err {
			return nil, werr
		}
		return nil, &ResponseError{Method: method, Path: path, Problem: "the request to " + c.host + " could not be completed", Err: err}
	}
	c.logger.Debug("docker request", "method", method, "path", path, "status", resp.StatusCode, "duration", since(start))
	// Podman announces itself on every response, so a client that pinned its
	// API version and never negotiates can still name the daemon in an error.
	c.noteRuntime(runtimeFromHeader(resp.Header))
	return resp, nil
}
