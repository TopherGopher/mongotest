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
	"reflect"
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
// query may be nil. The caller must close the response body.
//
// body may be nil for no body, an io.Reader whose bytes are sent as they
// are, or any value, which is JSON encoded. A reader is passed straight
// through rather than re-encoded: a caller that already holds the bytes
// should not have them marshalled again, and marshalling a reader would
// silently produce whatever its struct fields serialise to rather than an
// error.
//
// An encoded value is buffered so the daemon gets a Content-Length. Request
// bodies on this path are a few hundred bytes of JSON at most; anything
// large is a reader, and goes out chunked. Non-JSON payloads use doRaw.
func (c *Client) do(ctx context.Context, method, path string, query url.Values, body any) (*http.Response, error) {
	rdr, hasBody, err := requestBody(method, path, body)
	if err != nil {
		return nil, err
	}
	return c.doRaw(ctx, method, path, query, rdr, hasBody, "application/json")
}

// requestBody turns a caller's body into a stream. It reports hasBody
// separately, because a request with no body must not carry a Content-Type.
func requestBody(method, path string, body any) (io.Reader, bool, error) {
	if body == nil {
		return nil, false, nil
	}
	if rdr, ok := body.(io.Reader); ok {
		// A typed nil stored in an interface is not == nil, so a caller
		// passing a nil *strings.Reader would otherwise reach
		// http.NewRequest. Treat it as no body: before this it was JSON
		// encoded and the daemon received a literal null.
		if isNilValue(rdr) {
			return nil, false, nil
		}
		return rdr, true, nil
	}
	buf, err := json.Marshal(body)
	if err != nil {
		return nil, false, &ResponseError{Method: method, Path: path, Problem: "could not encode the request body", Err: err}
	}
	return bytes.NewReader(buf), true, nil
}

// isNilValue reports whether v holds a nil of a type that can be nil. An
// interface holding a typed nil compares unequal to nil, so this is the only
// way to recognise one.
func isNilValue(v any) bool {
	rv := reflect.ValueOf(v)
	switch rv.Kind() {
	case reflect.Pointer, reflect.Interface, reflect.Map, reflect.Slice, reflect.Func, reflect.Chan:
		return rv.IsNil()
	default:
		return false
	}
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
