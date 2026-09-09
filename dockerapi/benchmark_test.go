package dockerapi

import (
	"bytes"
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"testing"
)

// Benchmarks measure this client's own cost: building the request, encoding
// the body, and decoding the reply. The daemon is stubbed in-process (an
// http.RoundTripper, or a unix-socket fake where the endpoint hijacks the
// connection), so daemon and network time never enter the numbers.

// benchClient returns a client whose requests are answered by fn.
func benchClient(b *testing.B, fn func(*http.Request) (*http.Response, error)) *Client {
	b.Helper()
	return newMockClient(b, fn)
}

// benchNegotiated returns a client that has already negotiated, so the
// measured loop performs exactly one request per iteration.
func benchNegotiated(b *testing.B, fn func(*http.Request) (*http.Response, error)) *Client {
	b.Helper()
	c := benchClient(b, fn)
	if err := c.Negotiate(context.Background()); err != nil {
		b.Fatalf("negotiating against the stub: %v", err)
	}
	return c
}

// --- construction and options --------------------------------------------

func BenchmarkNew(b *testing.B) {
	b.ReportAllocs()
	for b.Loop() {
		if _, err := New(WithHost("unix:///var/run/docker.sock")); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkFromEnv(b *testing.B) {
	b.Setenv("DOCKER_HOST", "unix:///var/run/docker.sock")
	b.ReportAllocs()
	for b.Loop() {
		if _, err := FromEnv(); err != nil {
			b.Fatal(err)
		}
	}
}

// BenchmarkOptions covers the option constructors, which only close over
// their argument and are applied once per client.
func BenchmarkOptions(b *testing.B) {
	hc := &http.Client{}
	cfg := &tls.Config{MinVersion: tls.VersionTLS12}
	lg := NopLogger()
	opts := map[string]Option{
		"WithHost":       WithHost("unix:///var/run/docker.sock"),
		"WithHTTPClient": WithHTTPClient(hc),
		"WithTLSConfig":  WithTLSConfig(cfg),
		"WithUserAgent":  WithUserAgent("bench/1"),
		"WithLogger":     WithLogger(lg),
		"WithAPIVersion": WithAPIVersion("1.44"),
	}
	for name, opt := range opts {
		b.Run(name, func(b *testing.B) {
			b.ReportAllocs()
			c := &Client{}
			for b.Loop() {
				if err := opt(c); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

func BenchmarkNopLogger(b *testing.B) {
	b.ReportAllocs()
	for b.Loop() {
		_ = NopLogger()
	}
}

// --- accessors and negotiation -------------------------------------------

func BenchmarkClientHost(b *testing.B) {
	c := benchClient(b, mockStatus(http.StatusOK))
	b.ReportAllocs()
	for b.Loop() {
		_ = c.Host()
	}
}

func BenchmarkClientAPIVersion(b *testing.B) {
	c := benchNegotiated(b, mockStatus(http.StatusOK))
	b.ReportAllocs()
	for b.Loop() {
		_ = c.APIVersion()
	}
}

func BenchmarkClientNegotiate(b *testing.B) {
	b.Run("first call", func(b *testing.B) {
		// One GET /version per iteration; client construction is not timed.
		b.ReportAllocs()
		for b.Loop() {
			b.StopTimer()
			c := benchClient(b, mockStatus(http.StatusOK))
			b.StartTimer()
			if err := c.Negotiate(context.Background()); err != nil {
				b.Fatal(err)
			}
		}
	})
	b.Run("already negotiated", func(b *testing.B) {
		// The path every request takes: a mutex and a string compare.
		c := benchNegotiated(b, mockStatus(http.StatusOK))
		b.ReportAllocs()
		for b.Loop() {
			if err := c.Negotiate(context.Background()); err != nil {
				b.Fatal(err)
			}
		}
	})
}

// --- images ---------------------------------------------------------------

func BenchmarkImagePull(b *testing.B) {
	// The cost here is draining and decoding the progress stream, so scale
	// it by the number of progress lines the daemon sends.
	for _, lines := range []int{1, 100, 1000} {
		b.Run(fmt.Sprintf("%d-progress-lines", lines), func(b *testing.B) {
			var stream bytes.Buffer
			for i := range lines {
				fmt.Fprintf(&stream, `{"status":"Downloading","progressDetail":{"current":%d,"total":%d},"id":"layer"}`+"\n", i, lines)
			}
			body := stream.Bytes()
			c := benchNegotiated(b, func(req *http.Request) (*http.Response, error) {
				return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(bytes.NewReader(body)), Request: req}, nil
			})
			ctx := context.Background()
			b.SetBytes(int64(len(body)))
			b.ReportAllocs()
			for b.Loop() {
				if err := c.ImagePull(ctx, "mongo:8"); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

func BenchmarkImageInspect(b *testing.B) {
	c := benchNegotiated(b, mockJSON(http.StatusOK, ImageInspect{
		ID: "sha256:41c3b7abb48e", RepoTags: []string{"mongo:8"}, Architecture: "amd64", OS: "linux",
	}))
	ctx := context.Background()
	b.ReportAllocs()
	for b.Loop() {
		if _, err := c.ImageInspect(ctx, "mongo:8"); err != nil {
			b.Fatal(err)
		}
	}
}

// --- containers -----------------------------------------------------------

// benchContainerConfig is the shape mongod will send for every container.
func benchContainerConfig() ContainerConfig {
	return ContainerConfig{
		Image:        "mongo:8",
		Cmd:          []string{"--replSet", "rs0"},
		Labels:       map[string]string{"mongotest": "regression"},
		ExposedPorts: map[string]struct{}{"27017/tcp": {}},
		HostConfig: &HostConfig{PortBindings: map[string][]PortBinding{
			"27017/tcp": {{HostIP: "127.0.0.1", HostPort: ""}},
		}},
	}
}

func BenchmarkContainerCreate(b *testing.B) {
	c := benchNegotiated(b, mockJSON(http.StatusCreated, createResponse{ID: "c0ffee1234ab"}))
	cfg := benchContainerConfig()
	ctx := context.Background()
	b.ReportAllocs()
	for b.Loop() {
		if _, _, err := c.ContainerCreate(ctx, "mongotest-bench", cfg); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkContainerStart(b *testing.B) {
	c := benchNegotiated(b, mockStatus(http.StatusNoContent))
	ctx := context.Background()
	b.ReportAllocs()
	for b.Loop() {
		if err := c.ContainerStart(ctx, "c0ffee1234ab"); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkContainerRemove(b *testing.B) {
	c := benchNegotiated(b, mockStatus(http.StatusNoContent))
	ctx := context.Background()
	opts := RemoveOptions{Force: true, RemoveVolumes: true}
	b.ReportAllocs()
	for b.Loop() {
		if err := c.ContainerRemove(ctx, "c0ffee1234ab", opts); err != nil {
			b.Fatal(err)
		}
	}
}

// benchInspectBody is a realistic inspect reply for decode benchmarks.
const benchInspectBody = `{"Id":"c0ffee1234ab","Name":"/mongotest-bench",` +
	`"State":{"Status":"running","Running":true,"ExitCode":0},` +
	`"Config":{"Image":"mongo:8","Labels":{"mongotest":"regression"}},` +
	`"NetworkSettings":{"Ports":{"27017/tcp":[{"HostIp":"127.0.0.1","HostPort":"34819"}]}}}`

func benchInspectClient(b *testing.B) *Client {
	return benchNegotiated(b, func(req *http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(benchInspectBody)), Request: req}, nil
	})
}

func BenchmarkContainerInspect(b *testing.B) {
	c := benchInspectClient(b)
	ctx := context.Background()
	b.ReportAllocs()
	for b.Loop() {
		if _, err := c.ContainerInspect(ctx, "c0ffee1234ab"); err != nil {
			b.Fatal(err)
		}
	}
}

// BenchmarkContainerInspectParallel shows what happens when many callers
// share one client, which is how mongod will drive it from parallel tests.
func BenchmarkContainerInspectParallel(b *testing.B) {
	c := benchInspectClient(b)
	ctx := context.Background()
	b.ReportAllocs()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			if _, err := c.ContainerInspect(ctx, "c0ffee1234ab"); err != nil {
				b.Error(err)
				return
			}
		}
	})
}

func BenchmarkContainerTop(b *testing.B) {
	c := benchNegotiated(b, mockJSON(http.StatusOK, Top{
		Titles:    []string{"UID", "PID", "PPID", "C", "STIME", "TTY", "TIME", "CMD"},
		Processes: [][]string{{"999", "1234", "1200", "0", "23:02", "?", "00:00:01", "mongod --bind_ip_all"}},
	}))
	ctx := context.Background()
	b.ReportAllocs()
	for b.Loop() {
		if _, err := c.ContainerTop(ctx, "c0ffee1234ab"); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkContainerInspectHostPort(b *testing.B) {
	info := ContainerInspect{}
	info.NetworkSettings.Ports = map[string][]PortBinding{
		"27017/tcp": {{HostIP: "127.0.0.1", HostPort: "34819"}},
	}
	b.ReportAllocs()
	for b.Loop() {
		if info.HostPort("27017/tcp") == "" {
			b.Fatal("expected a published port")
		}
	}
}

// --- archives -------------------------------------------------------------

func BenchmarkCopyToContainer(b *testing.B) {
	// Dominated by building and streaming the tar, so scale by payload size.
	for _, size := range []int{1 << 10, 64 << 10, 1 << 20} {
		b.Run(fmt.Sprintf("%dKiB", size>>10), func(b *testing.B) {
			files := []File{
				{Name: "mongo-tls/server.pem", Mode: 0o644, Content: bytes.Repeat([]byte("x"), size)},
				{Name: "mongo-tls/ca.pem", Mode: 0o644, Content: bytes.Repeat([]byte("y"), 2048)},
			}
			c := benchNegotiated(b, func(req *http.Request) (*http.Response, error) {
				// Consume the streamed archive like the daemon would.
				if _, err := io.Copy(io.Discard, req.Body); err != nil {
					return nil, err
				}
				return &http.Response{StatusCode: http.StatusOK, Body: http.NoBody, Request: req}, nil
			})
			ctx := context.Background()
			b.SetBytes(int64(size + 2048))
			b.ReportAllocs()
			for b.Loop() {
				if err := c.CopyToContainer(ctx, "c0ffee1234ab", "/etc", files); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

func BenchmarkCopyArchiveToContainer(b *testing.B) {
	var archive bytes.Buffer
	if err := writeTar(&archive, []File{{Name: "seed.js", Content: bytes.Repeat([]byte("z"), 64<<10)}}); err != nil {
		b.Fatal(err)
	}
	body := archive.Bytes()
	c := benchNegotiated(b, func(req *http.Request) (*http.Response, error) {
		if _, err := io.Copy(io.Discard, req.Body); err != nil {
			return nil, err
		}
		return &http.Response{StatusCode: http.StatusOK, Body: http.NoBody, Request: req}, nil
	})
	ctx := context.Background()
	b.SetBytes(int64(len(body)))
	b.ReportAllocs()
	for b.Loop() {
		if err := c.CopyArchiveToContainer(ctx, "c0ffee1234ab", "/tmp", bytes.NewReader(body)); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkWriteTar(b *testing.B) {
	files := []File{
		{Name: "mongo-tls/server.pem", Mode: 0o644, Content: bytes.Repeat([]byte("x"), 4096)},
		{Name: "mongo-tls/ca.pem", Mode: 0o644, Content: bytes.Repeat([]byte("y"), 2048)},
	}
	b.SetBytes(4096 + 2048)
	b.ReportAllocs()
	for b.Loop() {
		if err := writeTar(io.Discard, files); err != nil {
			b.Fatal(err)
		}
	}
}

// --- exec -----------------------------------------------------------------

func BenchmarkExecCreate(b *testing.B) {
	c := benchNegotiated(b, mockJSON(http.StatusCreated, execCreateResponse{ID: "e5ec1d"}))
	cfg := ExecConfig{Cmd: []string{"mongosh", "--quiet", "--eval", "db.runCommand({ping:1}).ok"}}
	ctx := context.Background()
	b.ReportAllocs()
	for b.Loop() {
		if _, err := c.ExecCreate(ctx, "c0ffee1234ab", cfg); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkExecInspect(b *testing.B) {
	c := benchNegotiated(b, mockJSON(http.StatusOK, ExecInspect{ID: "e5ec1d", Running: false, ExitCode: 0}))
	ctx := context.Background()
	b.ReportAllocs()
	for b.Loop() {
		if _, err := c.ExecInspect(ctx, "e5ec1d"); err != nil {
			b.Fatal(err)
		}
	}
}

// execBenchClient wires a unix-socket fake daemon, because exec start
// hijacks the connection and cannot go through an http.RoundTripper. output
// is the number of bytes the fake writes on stdout per call.
func execBenchClient(b *testing.B, output int) *Client {
	b.Helper()
	c, err := New(WithHost(execFakeServer(b, output)))
	if err != nil {
		b.Fatal(err)
	}
	if err := c.Negotiate(context.Background()); err != nil {
		b.Fatal(err)
	}
	return c
}

func BenchmarkExecStart(b *testing.B) {
	for _, size := range []int{64, 64 << 10} {
		b.Run(fmt.Sprintf("%dB-output", size), func(b *testing.B) {
			c := execBenchClient(b, size)
			ctx := context.Background()
			b.SetBytes(int64(size))
			b.ReportAllocs()
			for b.Loop() {
				if _, _, err := c.ExecStart(ctx, "e5ec1d"); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

func BenchmarkExecStartTo(b *testing.B) {
	// The streaming form: output goes straight to a writer, never collected.
	c := execBenchClient(b, 64<<10)
	ctx := context.Background()
	b.SetBytes(64 << 10)
	b.ReportAllocs()
	for b.Loop() {
		if err := c.ExecStartTo(ctx, "e5ec1d", io.Discard, io.Discard); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkExec(b *testing.B) {
	// Create, start and inspect: what a caller pays for one command.
	c := execBenchClient(b, 64)
	ctx := context.Background()
	b.ReportAllocs()
	for b.Loop() {
		res, err := c.Exec(ctx, "c0ffee1234ab", "mongosh", "--quiet", "--eval", "1")
		if err != nil {
			b.Fatal(err)
		}
		if res.ExitCode != 0 {
			b.Fatalf("unexpected exit code %d", res.ExitCode)
		}
	}
}

func BenchmarkDemux(b *testing.B) {
	// The multiplexed stream decoder on its own, with stdout and stderr
	// interleaved in 32 KiB frames.
	var stream bytes.Buffer
	payload := strings.Repeat("x", 32<<10)
	for range 16 {
		stream.Write(benchFrame(streamStdout, payload))
		stream.Write(benchFrame(streamStderr, "warning\n"))
	}
	body := stream.Bytes()
	b.SetBytes(int64(len(body)))
	b.ReportAllocs()
	for b.Loop() {
		if err := demux(bytes.NewReader(body), io.Discard, io.Discard); err != nil {
			b.Fatal(err)
		}
	}
}

// --- errors ---------------------------------------------------------------

func BenchmarkErrorMessages(b *testing.B) {
	cases := map[string]error{
		"StatusError":          &StatusError{StatusCode: 404, Message: "No such container: x", Method: "GET", Path: "/containers/x/json"},
		"InvalidArgumentError": invalidArg("image reference", "Mongo:8", "repository names must be lowercase", "use the documented reference form"),
		"ConnectionError":      wrapConnError("unix:///var/run/docker.sock", errors.New("connection refused")),
		"APIVersionError":      &APIVersionError{Feature: "ExecConfig.WorkingDir", Required: "1.35", Negotiated: "1.30"},
		"ResponseError":        decodeError("GET", "/containers/x/json", io.ErrUnexpectedEOF),
		"StreamError":          &StreamError{Problem: "reading an exec stream frame header", Err: io.ErrUnexpectedEOF},
		"PullError":            &PullError{Ref: "mongo:nope", Message: "manifest unknown"},
	}
	for name, err := range cases {
		b.Run(name, func(b *testing.B) {
			b.ReportAllocs()
			for b.Loop() {
				_ = err.Error()
			}
		})
	}
}

func BenchmarkErrorMatching(b *testing.B) {
	// What callers do on every result: errors.Is against a sentinel.
	statusErr := &StatusError{StatusCode: 404, Message: "No such container: x", Method: "GET", Path: "/x"}
	cases := map[string]struct {
		err    error
		target error
	}{
		"IsNotFound":                 {statusErr, ErrNotFound},
		"StatusError.Is/conflict":    {statusErr, ErrConflict},
		"InvalidArgumentError.Is":    {ErrNoCommand, ErrInvalidArgument},
		"ConnectionError.Is":         {ErrNoWindowsEndpoint, ErrConnectionFailed},
		"APIVersionError.Is":         {&APIVersionError{ServerMin: "1.60"}, ErrAPIVersion},
		"ResponseError.Is":           {decodeError("GET", "/x", io.EOF), ErrDaemonResponse},
		"StreamError.Is":             {&StreamError{Problem: "x"}, ErrStream},
		"PullError.Is":               {&PullError{Ref: "mongo"}, ErrPull},
		"wrapped through fmt.Errorf": {fmt.Errorf("starting mongod: %w", statusErr), ErrNotFound},
	}
	for name, tc := range cases {
		b.Run(name, func(b *testing.B) {
			b.ReportAllocs()
			for b.Loop() {
				_ = errors.Is(tc.err, tc.target)
			}
		})
	}
}

func BenchmarkIsNotFound(b *testing.B) {
	err := &StatusError{StatusCode: 404, Message: "No such container: x", Method: "GET", Path: "/x"}
	b.ReportAllocs()
	for b.Loop() {
		if !IsNotFound(err) {
			b.Fatal("expected a not-found error")
		}
	}
}

func BenchmarkStatusErrorHint(b *testing.B) {
	err := &StatusError{StatusCode: 409, Message: "in use", Method: "DELETE", Path: "/containers/x"}
	b.ReportAllocs()
	for b.Loop() {
		if err.Hint() == "" {
			b.Fatal("expected a hint for 409")
		}
	}
}

func BenchmarkErrorUnwrap(b *testing.B) {
	cases := map[string]error{
		"ConnectionError": &ConnectionError{Host: "h", Problem: "p", Err: io.EOF},
		"ResponseError":   decodeError("GET", "/x", io.EOF),
		"StreamError":     &StreamError{Problem: "p", Err: io.EOF},
		"PullError":       &PullError{Ref: "mongo", Message: "m", Err: io.EOF},
	}
	for name, err := range cases {
		b.Run(name, func(b *testing.B) {
			b.ReportAllocs()
			for b.Loop() {
				_ = errors.Unwrap(err)
			}
		})
	}
}

// --- parsing and validation ----------------------------------------------

func BenchmarkParsing(b *testing.B) {
	b.Run("parseImageRef", func(b *testing.B) {
		b.ReportAllocs()
		for b.Loop() {
			if _, err := parseImageRef("localhost:5000/team/mongo:8.0-noble"); err != nil {
				b.Fatal(err)
			}
		}
	})
	b.Run("parseHost", func(b *testing.B) {
		b.ReportAllocs()
		for b.Loop() {
			if _, err := parseHost("unix:///var/run/docker.sock"); err != nil {
				b.Fatal(err)
			}
		}
	})
	b.Run("checkID", func(b *testing.B) {
		b.ReportAllocs()
		for b.Loop() {
			if _, err := checkID("container", "de686f1e8de9bcabdc4b20b2d9b80d33f90ed79aa7588ad907d953ad63dc9d3c"); err != nil {
				b.Fatal(err)
			}
		}
	})
	b.Run("ContainerConfig.validate", func(b *testing.B) {
		cfg := benchContainerConfig()
		b.ReportAllocs()
		for b.Loop() {
			if err := cfg.Validate(); err != nil {
				b.Fatal(err)
			}
		}
	})
	b.Run("compareVersions", func(b *testing.B) {
		b.ReportAllocs()
		for b.Loop() {
			_ = compareVersions("1.44", "1.41")
		}
	})
}

// --- exec fake ------------------------------------------------------------

// benchFrame builds one multiplexed stream frame for the fake daemon.
func benchFrame(stream byte, payload string) []byte {
	hdr := make([]byte, 8)
	hdr[0] = stream
	hdr[4] = byte(len(payload) >> 24)
	hdr[5] = byte(len(payload) >> 16)
	hdr[6] = byte(len(payload) >> 8)
	hdr[7] = byte(len(payload))
	return append(hdr, payload...)
}

// execFakeServer starts a unix-socket fake that answers the three exec
// endpoints, writing output bytes on stdout for each start. It returns the
// host string for WithHost.
func execFakeServer(b *testing.B, output int) string {
	b.Helper()
	payload := strings.Repeat("x", output)
	srv := newBenchFake(b)
	srv.handle("GET", "/version", func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, `{"ApiVersion":"1.44","MinAPIVersion":"1.24"}`)
	})
	srv.handle("POST", "exec-create", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusCreated)
		io.WriteString(w, `{"Id":"e5ec1d"}`)
	})
	srv.handle("GET", "exec-json", func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, `{"ID":"e5ec1d","Running":false,"ExitCode":0}`)
	})
	srv.handle("POST", "exec-start", func(w http.ResponseWriter, r *http.Request) {
		conn, rw, err := w.(http.Hijacker).Hijack()
		if err != nil {
			return
		}
		defer conn.Close()
		rw.WriteString("HTTP/1.1 101 UPGRADED\r\nContent-Type: application/vnd.docker.multiplexed-stream\r\n\r\n")
		rw.Write(benchFrame(streamStdout, payload))
		rw.Flush()
	})
	return srv.host
}

// benchFake is a tiny unix-socket HTTP server for the exec benchmarks.
// dockermock.Daemon is not used here because these benchmarks route by
// endpoint kind rather than by exact path, and want the smallest possible
// handler so the measurement is this client's cost rather than the double's.
type benchFake struct {
	host     string
	handlers map[string]http.HandlerFunc
}

func newBenchFake(b *testing.B) *benchFake {
	b.Helper()
	f := &benchFake{handlers: map[string]http.HandlerFunc{}}
	dir := b.TempDir()
	sock := dir + "/docker.sock"
	l, err := net.Listen("unix", sock)
	if err != nil {
		b.Fatalf("listening on %s: %v", sock, err)
	}
	srv := &http.Server{Handler: http.HandlerFunc(f.serve)}
	go srv.Serve(l)
	b.Cleanup(func() { _ = srv.Close() })
	f.host = "unix://" + sock
	return f
}

func (f *benchFake) handle(method, kind string, h http.HandlerFunc) {
	f.handlers[method+" "+kind] = h
}

func (f *benchFake) serve(w http.ResponseWriter, r *http.Request) {
	path := r.URL.Path
	if rest := strings.SplitN(strings.TrimPrefix(path, "/"), "/", 2); len(rest) == 2 && strings.HasPrefix(rest[0], "v1.") {
		path = "/" + rest[1]
	}
	kind := path
	switch {
	case strings.HasSuffix(path, "/exec") && strings.HasPrefix(path, "/containers/"):
		kind = "exec-create"
	case strings.HasPrefix(path, "/exec/") && strings.HasSuffix(path, "/start"):
		kind = "exec-start"
	case strings.HasPrefix(path, "/exec/") && strings.HasSuffix(path, "/json"):
		kind = "exec-json"
	}
	if h, ok := f.handlers[r.Method+" "+kind]; ok {
		h(w, r)
		return
	}
	w.WriteHeader(http.StatusNotFound)
	io.WriteString(w, `{"message":"page not found"}`)
}

// Runtime and ServerProduct are read on the error path, so they are cheap
// lock-and-copy accessors rather than anything that touches the daemon.

func BenchmarkClientRuntime(b *testing.B) {
	c := benchNegotiated(b, mockStatus(http.StatusOK))
	b.ReportAllocs()
	for b.Loop() {
		_ = c.Runtime()
	}
}

func BenchmarkClientServerProduct(b *testing.B) {
	c := benchNegotiated(b, mockStatus(http.StatusOK))
	b.ReportAllocs()
	for b.Loop() {
		_ = c.ServerProduct()
	}
}

// Discovery runs once per client built with FromEnv, and it dials up to five
// sockets before giving up, so its cost is worth watching.
func BenchmarkSocketCandidates(b *testing.B) {
	l := lookup{goos: "linux", getenv: func(k string) string {
		if k == "XDG_RUNTIME_DIR" {
			return "/run/user/1000"
		}
		return ""
	}, uid: 1000}
	b.ReportAllocs()
	for b.Loop() {
		_ = socketCandidates(l)
	}
}

func BenchmarkDetectRuntime(b *testing.B) {
	v := versionResponse{
		APIVersion: "1.41", MinAPIVersion: "1.24",
		Platform: versionPlatform{Name: "linux/amd64/fedora-40"},
		Components: []versionComponent{
			{Name: "Podman Engine", Version: "5.7.1"},
			{Name: "Conmon", Version: "2.1.12"},
			{Name: "OCI Runtime (crun)", Version: "1.15"},
		},
	}
	b.ReportAllocs()
	for b.Loop() {
		if rt, _ := detectRuntime(v, ""); rt != RuntimePodman {
			b.Fatalf("the component list must identify podman, got %q", rt)
		}
	}
}
