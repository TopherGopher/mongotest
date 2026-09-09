package dockermock

import (
	"context"
	"encoding/binary"
	"encoding/json/v2"
	"errors"
	"fmt"
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

	"github.com/tophergopher/mongotest/dockerclient"
)

// Fixed values the default routes answer with, so tests and examples can
// assert on them without repeating literals.
const (
	// DefaultAPIVersion is the API version ServeDefaults advertises.
	DefaultAPIVersion = "1.44"
	// DefaultMinAPIVersion is the oldest version it claims to accept.
	DefaultMinAPIVersion = "1.24"
	// DefaultDockerVersion is the engine version ServeVersion reports.
	DefaultDockerVersion = "29.3.1"
	// DefaultDockerProduct is what ServeVersion puts in Platform.Name, the
	// way a docker-ce package build does.
	DefaultDockerProduct = "Docker Engine - Community"
	// DefaultPodmanPlatform is what ServePodmanVersion puts in
	// Platform.Name: Podman reports a platform triple there, not a product.
	DefaultPodmanPlatform = "linux/amd64/fedora-40"
	// DefaultContainerID is the id ServeDefaults returns from a create.
	DefaultContainerID = "c0ffee1234ab"
	// DefaultContainerName is the name it reports from an inspect, with the
	// leading slash the daemon uses.
	DefaultContainerName = "/mongotest-example"
	// DefaultHostPort is the published host port it reports.
	DefaultHostPort = "34819"
	// DefaultExecID is the exec instance id it returns.
	DefaultExecID = "e5ec1d"
	// DefaultImageID is the image id it reports.
	DefaultImageID = "sha256:41c3b7abb48e"
	// DefaultExecOutput is what an exec started against it writes on stdout.
	DefaultExecOutput = "1\n"
)

// Daemon is a fake Docker daemon that speaks the Engine API over HTTP, so a
// real client can be pointed at it with its WithHost option. It records
// every request it receives.
//
// Route patterns may contain {name} placeholders, readable with PathParam. A
// leading API version prefix such as /v1.44 is stripped before matching, so
// a route works both before and after version negotiation. Unmatched
// requests get the daemon's own 404 body.
//
// The caller closes it, usually with defer or t.Cleanup:
//
//	d := dockermock.NewDaemon()
//	defer d.Close()
type Daemon struct {
	srv    *httptest.Server
	host   string
	tmpDir string

	mu     sync.Mutex
	routes []route
	reqs   []Request
}

// Request is one request as the Daemon saw it.
type Request struct {
	Method string
	// Path has any leading /v1.xx prefix removed.
	Path string
	// RawPath is the path exactly as received.
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

var versionPrefix = regexp.MustCompile(`^/v[0-9]+\.[0-9]+(/|$)`)

// DaemonOption configures a Daemon.
type DaemonOption func(*daemonConfig)

type daemonConfig struct{ tcp bool }

// OverTCP makes the Daemon listen on a loopback TCP port instead of a unix
// socket. Use it to exercise the TCP transport, or on a platform without
// unix sockets.
func OverTCP() DaemonOption { return func(c *daemonConfig) { c.tcp = true } }

// NewDaemon starts a fake daemon with no routes. It panics if it cannot
// listen, since a test double that fails to start has nothing useful to
// report.
func NewDaemon(opts ...DaemonOption) *Daemon {
	var cfg daemonConfig
	for _, opt := range opts {
		opt(&cfg)
	}
	d := &Daemon{}
	var l net.Listener
	var err error
	if cfg.tcp {
		l, err = net.Listen("tcp", "127.0.0.1:0")
		if err == nil {
			d.host = "tcp://" + l.Addr().String()
		}
	} else {
		// Keep the path short: a unix socket path is limited to about 100
		// bytes, which a nested temp directory can exceed.
		d.tmpDir, err = os.MkdirTemp("", "dockermock")
		if err == nil {
			sock := filepath.Join(d.tmpDir, "docker.sock")
			l, err = net.Listen("unix", sock)
			d.host = "unix://" + sock
		}
	}
	if err != nil {
		panic("dockermock: cannot start the fake daemon: " + err.Error())
	}
	d.srv = httptest.NewUnstartedServer(http.HandlerFunc(d.serve))
	_ = d.srv.Listener.Close() // replace the listener httptest opened for us
	d.srv.Listener = l
	d.srv.Start()
	return d
}

// Close shuts the daemon down and removes its socket.
func (d *Daemon) Close() {
	d.srv.Close()
	if d.tmpDir != "" {
		_ = os.RemoveAll(d.tmpDir)
	}
}

// Host returns the address in DOCKER_HOST form, ready for a client's
// WithHost option.
func (d *Daemon) Host() string { return d.host }

// URL returns the plain http base URL, which is useful for a TCP daemon.
func (d *Daemon) URL() string { return d.srv.URL }

// Handle registers a handler for a method and path pattern such as
// "/containers/{id}/json". Registering the same route again replaces it.
func (d *Daemon) Handle(method, pattern string, h http.HandlerFunc) {
	d.mu.Lock()
	defer d.mu.Unlock()
	r := route{method: strings.ToUpper(method), segs: splitPath(pattern), h: h}
	for i, existing := range d.routes {
		if existing.method == r.method && strings.Join(existing.segs, "/") == strings.Join(r.segs, "/") {
			d.routes[i] = r
			return
		}
	}
	d.routes = append(d.routes, r)
}

// Requests returns every request received so far, in order.
func (d *Daemon) Requests() []Request {
	d.mu.Lock()
	defer d.mu.Unlock()
	out := make([]Request, len(d.reqs))
	copy(out, d.reqs)
	return out
}

// Reset forgets the recorded requests. Routes are kept.
func (d *Daemon) Reset() {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.reqs = nil
}

// ServeVersion registers the GET /version route clients negotiate against,
// answering the way a Docker Engine does: a component named "Engine" and a
// product name in Platform.Name. minVersion may be empty, which is how old
// daemons answer.
func (d *Daemon) ServeVersion(apiVersion, minVersion string) {
	d.Handle("GET", "/version", func(w http.ResponseWriter, _ *http.Request) {
		body := map[string]any{
			"Version":    DefaultDockerVersion,
			"ApiVersion": apiVersion,
			"Platform":   map[string]string{"Name": DefaultDockerProduct},
			"Components": []map[string]string{
				{"Name": "Engine", "Version": DefaultDockerVersion},
				{"Name": "containerd", "Version": "1.7.27"},
			},
		}
		if minVersion != "" {
			body["MinAPIVersion"] = minVersion
		}
		JSON(w, http.StatusOK, body)
	})
}

// ServePodmanVersion registers a GET /version route that answers the way
// Podman's Docker-compatible endpoint does: a component named "Podman
// Engine", a goos/goarch/distribution string in Platform.Name rather than a
// product name, and the Libpod-API-Version response header that only Podman
// sets.
//
// Use it to exercise the paths a caller takes against Podman without having
// Podman installed. Podman capped the compatible API at 1.41 from 4.x
// through 5.7 and raised it to 1.44 in 5.8, so passing 1.41 here is what
// tests a client's willingness to negotiate downwards.
func (d *Daemon) ServePodmanVersion(apiVersion, libpodVersion string) {
	d.Handle("GET", "/version", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Libpod-API-Version", libpodVersion)
		w.Header().Set("Server", "Libpod/"+libpodVersion+" (linux)")
		JSON(w, http.StatusOK, map[string]any{
			"Version":       libpodVersion,
			"ApiVersion":    apiVersion,
			"MinAPIVersion": DefaultMinAPIVersion,
			"Platform":      map[string]string{"Name": DefaultPodmanPlatform},
			"Components": []map[string]string{
				{"Name": "Podman Engine", "Version": libpodVersion},
				{"Name": "Conmon", "Version": "2.1.12"},
				{"Name": "OCI Runtime (crun)", "Version": "1.15"},
			},
		})
	})
}

// ServeDefaults registers a working route for every endpoint the Client
// interface covers, answering with the Default* values above. It is what
// examples use: enough of a daemon that a full lifecycle succeeds, with
// output that never varies.
//
// Two ids behave specially so that failures can be demonstrated too: an id
// beginning "no-such" answers 404, and the exec id "brokenstream" writes a
// daemon error frame instead of output.
func (d *Daemon) ServeDefaults() {
	d.ServeVersion(DefaultAPIVersion, DefaultMinAPIVersion)

	d.Handle("POST", "/images/create", func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Query().Get("tag"), "nope") {
			// A failed pull is reported inside a 200 stream, not by status.
			fmt.Fprintln(w, `{"status":"Pulling from library/mongo"}`)
			fmt.Fprintln(w, `{"errorDetail":{"message":"manifest for mongo:nope not found: manifest unknown"},`+
				`"error":"manifest for mongo:nope not found: manifest unknown"}`)
			return
		}
		fmt.Fprintln(w, `{"status":"Pulling from library/mongo"}`)
		fmt.Fprintln(w, `{"status":"Status: Downloaded newer image for mongo:8"}`)
	})
	d.Handle("GET", "/images/{ref}/json", func(w http.ResponseWriter, r *http.Request) {
		JSON(w, http.StatusOK, map[string]any{
			"Id": DefaultImageID, "RepoTags": []string{"mongo:8"}, "Architecture": "amd64", "Os": "linux",
		})
	})
	d.Handle("POST", "/containers/create", func(w http.ResponseWriter, r *http.Request) {
		JSON(w, http.StatusCreated, map[string]any{"Id": DefaultContainerID, "Warnings": []string{}})
	})
	d.Handle("POST", "/containers/{id}/start", func(w http.ResponseWriter, r *http.Request) {
		if id := PathParam(r, "id"); strings.HasPrefix(id, "no-such") {
			Error(w, http.StatusNotFound, "No such container: "+id)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})
	d.Handle("DELETE", "/containers/{id}", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})
	d.Handle("GET", "/containers/{id}/json", func(w http.ResponseWriter, r *http.Request) {
		switch id := PathParam(r, "id"); {
		case strings.HasPrefix(id, "no-such"):
			Error(w, http.StatusNotFound, "No such container: "+id)
		case id == "badjson":
			// What a version mismatch tends to look like from a client.
			io.WriteString(w, `{"Id": not json`)
		default:
			io.WriteString(w, `{"Id":"`+DefaultContainerID+`","Name":"`+DefaultContainerName+`",`+
				`"State":{"Status":"running","Running":true,"ExitCode":0},`+
				`"Config":{"Image":"mongo:8","Labels":{"mongotest":"regression"}},`+
				`"NetworkSettings":{"Ports":{"27017/tcp":[{"HostIp":"127.0.0.1","HostPort":"`+DefaultHostPort+`"}]}}}`)
		}
	})
	d.Handle("GET", "/containers/{id}/top", func(w http.ResponseWriter, r *http.Request) {
		JSON(w, http.StatusOK, map[string]any{
			"Titles": []string{"PID", "CMD"}, "Processes": [][]string{{"1", "mongod --bind_ip_all"}},
		})
	})
	d.Handle("PUT", "/containers/{id}/archive", func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		w.WriteHeader(http.StatusOK)
	})
	d.Handle("POST", "/containers/{id}/exec", func(w http.ResponseWriter, r *http.Request) {
		JSON(w, http.StatusCreated, map[string]any{"Id": DefaultExecID})
	})
	d.Handle("POST", "/exec/{id}/start", func(w http.ResponseWriter, r *http.Request) {
		stream := Frame(StreamStdout, DefaultExecOutput)
		if PathParam(r, "id") == "brokenstream" {
			stream = Frame(StreamSystemErr, "container is not running")
		}
		Hijack(w, stream)
	})
	d.Handle("GET", "/exec/{id}/json", func(w http.ResponseWriter, r *http.Request) {
		JSON(w, http.StatusOK, map[string]any{"ID": DefaultExecID, "Running": false, "ExitCode": 0})
	})
}

type paramsKey struct{}

// PathParam returns the value a {name} placeholder matched in the route.
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
	_ = json.MarshalWrite(w, v)
}

// Error writes the daemon's standard error body, {"message": msg}.
func Error(w http.ResponseWriter, status int, msg string) {
	JSON(w, status, map[string]string{"message": msg})
}

// Stream types in the daemon's multiplexed output format.
const (
	StreamStdout byte = 1
	StreamStderr byte = 2
	// StreamSystemErr carries an error from the daemon itself, for example
	// when the container stops in the middle of an exec.
	StreamSystemErr byte = 3
)

// Frame builds one frame of the multiplexed output stream: the stream type,
// three zero bytes, then the payload length as a big-endian uint32. Exec
// output is framed this way when no TTY is allocated, which is how stdout
// and stderr stay separable on one connection.
func Frame(stream byte, payload string) []byte {
	hdr := make([]byte, 8)
	hdr[0] = stream
	binary.BigEndian.PutUint32(hdr[4:], uint32(len(payload)))
	return append(hdr, payload...)
}

// Hijack takes over the connection, answers with the 101 upgrade a real
// daemon sends for an exec start, and writes the given stream bytes. Exec
// start cannot be served through a normal ResponseWriter, because the daemon
// reuses the connection as a raw byte stream once the headers are sent.
func Hijack(w http.ResponseWriter, stream []byte) {
	hj, ok := w.(http.Hijacker)
	if !ok {
		Error(w, http.StatusInternalServerError, "the test server cannot hijack connections")
		return
	}
	conn, rw, err := hj.Hijack()
	if err != nil {
		return
	}
	defer conn.Close()
	rw.WriteString("HTTP/1.1 101 UPGRADED\r\n" +
		"Content-Type: application/vnd.docker.multiplexed-stream\r\n" +
		"Connection: Upgrade\r\nUpgrade: tcp\r\n\r\n")
	rw.Write(stream)
	rw.Flush()
}

func (d *Daemon) serve(w http.ResponseWriter, r *http.Request) {
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
	d.mu.Lock()
	d.reqs = append(d.reqs, Request{
		Method: r.Method, Path: path, RawPath: r.URL.Path,
		Query: r.URL.Query(), Header: r.Header.Clone(), Body: body,
	})
	routes := make([]route, len(d.routes))
	copy(routes, d.routes)
	d.mu.Unlock()

	segs := splitPath(path)
	for _, rt := range routes {
		if rt.method != r.Method {
			continue
		}
		if params, ok := matchPath(rt.segs, segs); ok {
			rt.h(w, r.WithContext(context.WithValue(r.Context(), paramsKey{}, params)))
			return
		}
	}
	Error(w, http.StatusNotFound, "page not found")
}

func splitPath(p string) []string {
	var out []string
	for _, s := range strings.Split(p, "/") {
		if s != "" {
			out = append(out, s)
		}
	}
	return out
}

func matchPath(pattern, segs []string) (map[string]string, bool) {
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

// ServeFake registers routes that delegate to a Fake, turning this daemon
// into a stateful one: a container created through the HTTP API really
// exists, starting it really makes it running, and removing it really makes
// later calls return 404.
//
// It is how a real client is held to the same behaviour as the in-memory
// doubles. The conformance suite in dockerclient/dockerclienttest runs
// against a Fake directly and against dockerapi and mobyclient pointed at a
// daemon wired this way, so all three must agree.
func (d *Daemon) ServeFake(f *Fake) {
	d.ServeVersion(DefaultAPIVersion, DefaultMinAPIVersion)
	ctx := context.Background()

	// fail turns an error from the Fake into the response a daemon would
	// send, so the client under test maps it back to the same error.
	fail := func(w http.ResponseWriter, err error) {
		var se *statusError
		if asStatusError(err, &se) {
			Error(w, se.code, se.message)
			return
		}
		Error(w, http.StatusBadRequest, err.Error())
	}

	d.Handle("POST", "/images/create", func(w http.ResponseWriter, r *http.Request) {
		ref := r.URL.Query().Get("fromImage")
		if tag := r.URL.Query().Get("tag"); tag != "" {
			ref += ":" + tag
		}
		if err := f.ImagePull(ctx, ref); err != nil {
			// A pull failure is reported inside a 200 stream.
			JSON(w, http.StatusOK, map[string]any{"error": err.Error(),
				"errorDetail": map[string]string{"message": err.Error()}})
			return
		}
		fmt.Fprintln(w, `{"status":"Status: Downloaded newer image for `+ref+`"}`)
	})
	d.Handle("GET", "/images/{ref}/json", func(w http.ResponseWriter, r *http.Request) {
		img, err := f.ImageInspect(ctx, PathParam(r, "ref"))
		if err != nil {
			fail(w, err)
			return
		}
		JSON(w, http.StatusOK, img)
	})
	d.Handle("POST", "/containers/create", func(w http.ResponseWriter, r *http.Request) {
		var cfg dockerclient.ContainerConfig
		if err := json.UnmarshalRead(r.Body, &cfg); err != nil {
			Error(w, http.StatusBadRequest, "cannot parse the container config: "+err.Error())
			return
		}
		id, warnings, err := f.ContainerCreate(ctx, r.URL.Query().Get("name"), cfg)
		if err != nil {
			fail(w, err)
			return
		}
		JSON(w, http.StatusCreated, map[string]any{"Id": id, "Warnings": warnings})
	})
	d.Handle("POST", "/containers/{id}/start", func(w http.ResponseWriter, r *http.Request) {
		if err := f.ContainerStart(ctx, PathParam(r, "id")); err != nil {
			fail(w, err)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})
	d.Handle("DELETE", "/containers/{id}", func(w http.ResponseWriter, r *http.Request) {
		opts := dockerclient.RemoveOptions{
			Force:         r.URL.Query().Get("force") == "1",
			RemoveVolumes: r.URL.Query().Get("v") == "1",
		}
		if err := f.ContainerRemove(ctx, PathParam(r, "id"), opts); err != nil {
			fail(w, err)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})
	d.Handle("GET", "/containers/{id}/json", func(w http.ResponseWriter, r *http.Request) {
		info, err := f.ContainerInspect(ctx, PathParam(r, "id"))
		if err != nil {
			fail(w, err)
			return
		}
		JSON(w, http.StatusOK, info)
	})
	d.Handle("GET", "/containers/{id}/top", func(w http.ResponseWriter, r *http.Request) {
		top, err := f.ContainerTop(ctx, PathParam(r, "id"))
		if err != nil {
			fail(w, err)
			return
		}
		JSON(w, http.StatusOK, top)
	})
	d.Handle("PUT", "/containers/{id}/archive", func(w http.ResponseWriter, r *http.Request) {
		err := f.CopyArchiveToContainer(ctx, PathParam(r, "id"), r.URL.Query().Get("path"), r.Body)
		if err != nil {
			fail(w, err)
			return
		}
		w.WriteHeader(http.StatusOK)
	})
	d.Handle("POST", "/containers/{id}/exec", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Cmd        []string `json:"Cmd"`
			Env        []string `json:"Env"`
			WorkingDir string   `json:"WorkingDir"`
		}
		if err := json.UnmarshalRead(r.Body, &body); err != nil {
			Error(w, http.StatusBadRequest, "cannot parse the exec config: "+err.Error())
			return
		}
		id, err := f.ExecCreate(ctx, PathParam(r, "id"),
			dockerclient.ExecConfig{Cmd: body.Cmd, Env: body.Env, WorkingDir: body.WorkingDir})
		if err != nil {
			fail(w, err)
			return
		}
		JSON(w, http.StatusCreated, map[string]any{"Id": id})
	})
	d.Handle("POST", "/exec/{id}/start", func(w http.ResponseWriter, r *http.Request) {
		var stdout, stderr strings.Builder
		if err := f.ExecStartTo(ctx, PathParam(r, "id"), &stdout, &stderr); err != nil {
			fail(w, err)
			return
		}
		var stream []byte
		if stdout.Len() > 0 {
			stream = append(stream, Frame(StreamStdout, stdout.String())...)
		}
		if stderr.Len() > 0 {
			stream = append(stream, Frame(StreamStderr, stderr.String())...)
		}
		Hijack(w, stream)
	})
	d.Handle("GET", "/exec/{id}/json", func(w http.ResponseWriter, r *http.Request) {
		ins, err := f.ExecInspect(ctx, PathParam(r, "id"))
		if err != nil {
			fail(w, err)
			return
		}
		JSON(w, http.StatusOK, ins)
	})
}

// statusError and asStatusError read the status code and message back out of
// a dockerclient.StatusError without this package importing an implementation.
type statusError struct {
	code    int
	message string
}

func asStatusError(err error, out **statusError) bool {
	var se *dockerclient.StatusError
	if !errorsAs(err, &se) {
		return false
	}
	*out = &statusError{code: se.StatusCode, message: se.Message}
	return true
}

// errorsAs is errors.As, named locally so the helper above reads clearly.
func errorsAs(err error, target any) bool { return errors.As(err, target) }
