// Package fakedaemon is a programmable fake Docker Engine API server for tests.
//
// It listens on a unix socket (New) or a loopback TCP port (NewTCP), records
// every request it receives, and dispatches to handlers registered with
// Handle. Route matching ignores a leading API version prefix such as
// "/v1.44" so tests written before and after version negotiation both work.
// Unmatched requests receive the daemon's usual 404 body.
package fakedaemon

import (
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"
)

// RecordedRequest is one request as seen by the fake daemon.
type RecordedRequest struct {
	Method string
	// Path is the request path with any leading "/v1.xx" prefix removed.
	Path string
	// RawPath is the request path exactly as received.
	RawPath string
	Query   url.Values
	Header  http.Header
	Body    []byte
}

type route struct {
	method string
	segs   []string
	h      http.HandlerFunc
}

// Server is a fake Docker daemon. Create one with New or NewTCP.
type Server struct {
	tb   testing.TB
	srv  *httptest.Server
	host string

	mu     sync.Mutex
	routes []route
	reqs   []RecordedRequest
}

var versionPrefix = regexp.MustCompile(`^/v[0-9]+\.[0-9]+(/|$)`)

// New starts a fake daemon on a unix socket inside a fresh temporary
// directory. It is closed, and the directory removed, when the test ends.
func New(tb testing.TB) *Server {
	tb.Helper()
	// Use a short path: unix socket paths are limited to about 100 bytes.
	dir, err := os.MkdirTemp("", "fakedocker")
	if err != nil {
		tb.Fatalf("fakedaemon: temp dir: %v", err)
	}
	tb.Cleanup(func() { _ = os.RemoveAll(dir) })
	sock := filepath.Join(dir, "docker.sock")
	l, err := net.Listen("unix", sock)
	if err != nil {
		tb.Fatalf("fakedaemon: listen unix %s: %v", sock, err)
	}
	return start(tb, l, "unix://"+sock)
}

// NewTCP starts a fake daemon on a random loopback TCP port.
func NewTCP(tb testing.TB) *Server {
	tb.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		tb.Fatalf("fakedaemon: listen tcp: %v", err)
	}
	return start(tb, l, "tcp://"+l.Addr().String())
}

func start(tb testing.TB, l net.Listener, host string) *Server {
	s := &Server{tb: tb, host: host}
	s.srv = httptest.NewUnstartedServer(http.HandlerFunc(s.serve))
	// NewUnstartedServer already opened a listener we do not want.
	_ = s.srv.Listener.Close()
	s.srv.Listener = l
	s.srv.Start()
	tb.Cleanup(s.srv.Close)
	return s
}

// Host returns the value to hand to the client under test, in DOCKER_HOST
// form: "unix:///path/docker.sock" or "tcp://127.0.0.1:port".
func (s *Server) Host() string { return s.host }

// URL returns the plain http base URL of the fake (useful for TCP servers).
func (s *Server) URL() string { return s.srv.URL }

// Handle registers a handler for method and a path pattern such as
// "/containers/{id}/json". A "{name}" segment matches exactly one path
// segment and is available to the handler through PathParam. Patterns are
// matched against the path with any version prefix removed.
func (s *Server) Handle(method, pattern string, h http.HandlerFunc) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.routes = append(s.routes, route{method: strings.ToUpper(method), segs: split(pattern), h: h})
}

// Requests returns a copy of every request received so far, in order.
func (s *Server) Requests() []RecordedRequest {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]RecordedRequest, len(s.reqs))
	copy(out, s.reqs)
	return out
}

// Reset forgets recorded requests (routes are kept).
func (s *Server) Reset() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.reqs = nil
}

type paramsKey struct{}

// PathParam returns the value matched by "{name}" in the route pattern.
func PathParam(r *http.Request, name string) string {
	if m, ok := r.Context().Value(paramsKey{}).(map[string]string); ok {
		return m[name]
	}
	return ""
}

// JSON writes v as a JSON response with the given status.
func JSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// Error writes the daemon's standard error body {"message": msg}.
func Error(w http.ResponseWriter, status int, msg string) {
	JSON(w, status, map[string]string{"message": msg})
}

func (s *Server) serve(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	_ = r.Body.Close()
	r.Body = io.NopCloser(strings.NewReader(string(body)))

	path := r.URL.Path
	if m := versionPrefix.FindStringIndex(path); m != nil {
		path = path[m[1]:]
		if !strings.HasPrefix(path, "/") {
			path = "/" + path
		}
	}
	rec := RecordedRequest{
		Method:  r.Method,
		Path:    path,
		RawPath: r.URL.Path,
		Query:   r.URL.Query(),
		Header:  r.Header.Clone(),
		Body:    body,
	}
	s.mu.Lock()
	s.reqs = append(s.reqs, rec)
	routes := make([]route, len(s.routes))
	copy(routes, s.routes)
	s.mu.Unlock()

	segs := split(path)
	for _, rt := range routes {
		if rt.method != r.Method {
			continue
		}
		if params, ok := match(rt.segs, segs); ok {
			ctx := context.WithValue(r.Context(), paramsKey{}, params)
			rt.h(w, r.WithContext(ctx))
			return
		}
	}
	Error(w, http.StatusNotFound, "page not found")
}

func split(p string) []string {
	var out []string
	for _, s := range strings.Split(p, "/") {
		if s != "" {
			out = append(out, s)
		}
	}
	return out
}

func match(pattern, segs []string) (map[string]string, bool) {
	if len(pattern) != len(segs) {
		return nil, false
	}
	params := map[string]string{}
	for i, p := range pattern {
		if strings.HasPrefix(p, "{") && strings.HasSuffix(p, "}") {
			params[p[1:len(p)-1]] = segs[i]
			continue
		}
		if p != segs[i] {
			return nil, false
		}
	}
	return params, true
}
