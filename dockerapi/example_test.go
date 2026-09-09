package dockerapi_test

import (
	"archive/tar"
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"

	"github.com/tophergopher/mongotest/dockerapi"
	"github.com/tophergopher/mongotest/dockermock"
)

// The examples in this file talk to an in-process stand-in for the Docker
// Engine API (see stubDaemon at the bottom of the file), so they run
// anywhere and produce the same output with no Docker daemon installed.
// Real code gets its client from dockerapi.FromEnv instead.

func ExampleFromEnv() {
	// FromEnv locates the daemon like the docker CLI: DOCKER_HOST, then the
	// current docker context, then the platform default socket.
	os.Setenv("DOCKER_HOST", stubHost())
	defer os.Unsetenv("DOCKER_HOST")

	c, err := dockerapi.FromEnv()
	if err != nil {
		// The message names the variable or setting to fix.
		fmt.Println("cannot reach a daemon:", err)
		return
	}
	fmt.Println("using the daemon from DOCKER_HOST:", c.Host() == os.Getenv("DOCKER_HOST"))
	// Output: using the daemon from DOCKER_HOST: true
}

func ExampleNew() {
	c, err := dockerapi.New(
		dockerapi.WithHost(stubHost()),
		dockerapi.WithUserAgent("my-tests/1.0"),
	)
	if err != nil {
		panic(err)
	}
	if err := c.Negotiate(context.Background()); err != nil {
		panic(err)
	}
	fmt.Println("negotiated API", c.APIVersion())
	// Output: negotiated API 1.44
}

func ExampleWithHost() {
	// A unix socket, a TCP endpoint, or an http/https URL.
	c, err := dockerapi.New(dockerapi.WithHost("unix:///var/run/docker.sock"))
	if err != nil {
		panic(err)
	}
	fmt.Println(c.Host())

	// A host with no scheme is refused before anything is dialled.
	_, err = dockerapi.New(dockerapi.WithHost("localhost:2375"))
	fmt.Println(errors.Is(err, dockerapi.ErrInvalidArgument))
	// Output:
	// unix:///var/run/docker.sock
	// true
}

func ExampleWithHTTPClient() {
	// Answer every request in-process, without a socket: useful for unit
	// tests of code that takes a *dockerapi.Client.
	stub := roundTripperFunc(func(req *http.Request) (*http.Response, error) {
		body := `{"ApiVersion":"1.44","MinAPIVersion":"1.24"}`
		if strings.HasSuffix(req.URL.Path, "/json") {
			body = `{"Id":"sha256:stub","RepoTags":["mongo:8"],"Os":"linux"}`
		}
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": {"application/json"}},
			Body:       io.NopCloser(strings.NewReader(body)),
			Request:    req,
		}, nil
	})
	c, err := dockerapi.New(
		dockerapi.WithHost("unix:///stub/docker.sock"),
		dockerapi.WithHTTPClient(&http.Client{Transport: stub}),
	)
	if err != nil {
		panic(err)
	}
	img, err := c.ImageInspect(context.Background(), "mongo:8")
	if err != nil {
		panic(err)
	}
	fmt.Println(img.ID)
	// Output: sha256:stub
}

type roundTripperFunc func(*http.Request) (*http.Response, error)

func (f roundTripperFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func ExampleWithTLSConfig() {
	// A daemon reachable over TLS: trust its CA explicitly. Without this
	// option the CA comes from DOCKER_CERT_PATH when DOCKER_TLS_VERIFY is set.
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, `{"ApiVersion":"1.44","MinAPIVersion":"1.24"}`)
	}))
	defer srv.Close()
	pool := x509.NewCertPool()
	pool.AddCert(srv.Certificate())

	c, err := dockerapi.New(
		dockerapi.WithHost("tcp://"+strings.TrimPrefix(srv.URL, "https://")),
		dockerapi.WithTLSConfig(&tls.Config{RootCAs: pool}),
	)
	if err != nil {
		panic(err)
	}
	if err := c.Negotiate(context.Background()); err != nil {
		panic(err)
	}
	fmt.Println("connected over TLS, API", c.APIVersion())
	// Output: connected over TLS, API 1.44
}

func ExampleWithUserAgent() {
	// Identify your tool in the daemon's logs.
	c, err := dockerapi.New(dockerapi.WithHost(stubHost()), dockerapi.WithUserAgent("mongotest/2.0"))
	if err != nil {
		panic(err)
	}
	fmt.Println(c.Negotiate(context.Background()))
	// Output: <nil>
}

// printLogger is a minimal dockerapi.Logger for the example below. A
// *slog.Logger satisfies the interface directly and is the usual choice.
type printLogger struct{}

func (printLogger) Debug(msg string, _ ...any) { fmt.Println("debug:", msg) }
func (printLogger) Info(msg string, _ ...any)  { fmt.Println("info:", msg) }
func (printLogger) Warn(msg string, _ ...any)  { fmt.Println("warn:", msg) }
func (printLogger) Error(msg string, _ ...any) { fmt.Println("error:", msg) }

func ExampleWithLogger() {
	// One debug record per request. Pass slog.Default() in real code.
	c, err := dockerapi.New(dockerapi.WithHost(stubHost()), dockerapi.WithLogger(printLogger{}))
	if err != nil {
		panic(err)
	}
	if err := c.Negotiate(context.Background()); err != nil {
		panic(err)
	}
	// Output: debug: docker request
}

func ExampleWithAPIVersion() {
	// Pin the Engine API version instead of negotiating it. The
	// DOCKER_API_VERSION environment variable has the same effect.
	c, err := dockerapi.New(dockerapi.WithHost(stubHost()), dockerapi.WithAPIVersion("1.43"))
	if err != nil {
		panic(err)
	}
	fmt.Println(c.APIVersion()) // known before any request is made

	_, err = dockerapi.New(dockerapi.WithAPIVersion("latest"))
	fmt.Println(errors.Is(err, dockerapi.ErrInvalidArgument))
	// Output:
	// 1.43
	// true
}

func ExampleNopLogger() {
	// The default logger, useful to reset one you set earlier.
	c, err := dockerapi.New(dockerapi.WithHost(stubHost()), dockerapi.WithLogger(dockerapi.NopLogger()))
	if err != nil {
		panic(err)
	}
	fmt.Println(c.Negotiate(context.Background())) // logs nothing
	// Output: <nil>
}

func ExampleClient_Host() {
	c := exampleClient()
	fmt.Println(strings.HasPrefix(c.Host(), "tcp://"))
	// Output: true
}

func ExampleClient_APIVersion() {
	c := exampleClient()
	fmt.Printf("before negotiation: %q\n", c.APIVersion())
	if err := c.Negotiate(context.Background()); err != nil {
		panic(err)
	}
	fmt.Printf("after negotiation: %q\n", c.APIVersion())
	// Output:
	// before negotiation: ""
	// after negotiation: "1.44"
}

func ExampleClient_Negotiate() {
	c := exampleClient()
	// Negotiation is implicit on the first request; call it directly to
	// check connectivity before doing any work.
	if err := c.Negotiate(context.Background()); err != nil {
		var ce *dockerapi.ConnectionError
		if errors.As(err, &ce) {
			fmt.Printf("no daemon at %s: %s\n", ce.Host, ce.Fix)
			return
		}
		panic(err)
	}
	fmt.Println("daemon speaks API", c.APIVersion())
	// Output: daemon speaks API 1.44
}

func ExampleClient_ImagePull() {
	c := exampleClient()
	if err := c.ImagePull(context.Background(), "mongo:8"); err != nil {
		if errors.Is(err, dockerapi.ErrUnauthorized) {
			fmt.Println("this registry needs credentials: run docker login")
			return
		}
		panic(err)
	}
	fmt.Println("mongo:8 is available locally")
	// Output: mongo:8 is available locally
}

func ExampleClient_ImageInspect() {
	c := exampleClient()
	img, err := c.ImageInspect(context.Background(), "mongo:8")
	if dockerapi.IsNotFound(err) {
		fmt.Println("mongo:8 has not been pulled yet")
		return
	}
	if err != nil {
		panic(err)
	}
	fmt.Println(img.ID, img.RepoTags, img.Architecture, img.OS)
	// Output: sha256:41c3b7abb48e [mongo:8] amd64 linux
}

func ExampleClient_ContainerCreate() {
	c := exampleClient()
	ctx := context.Background()
	cfg := dockerapi.ContainerConfig{
		Image:        "mongo:8",
		Cmd:          []string{"--replSet", "rs0"}, // the image entrypoint prepends mongod
		Labels:       map[string]string{"mongotest": "regression"},
		ExposedPorts: map[string]struct{}{"27017/tcp": {}},
		HostConfig: &dockerapi.HostConfig{PortBindings: map[string][]dockerapi.PortBinding{
			// An empty HostPort lets the daemon pick a free one; read it
			// back with ContainerInspect.
			"27017/tcp": {{HostIP: "127.0.0.1", HostPort: ""}},
		}},
	}
	id, warnings, err := c.ContainerCreate(ctx, "mongotest-example", cfg)
	if dockerapi.IsNotFound(err) {
		// The image is not local yet: pull once, then retry.
		if err = c.ImagePull(ctx, cfg.Image); err != nil {
			panic(err)
		}
		id, warnings, err = c.ContainerCreate(ctx, "mongotest-example", cfg)
	}
	if err != nil {
		panic(err)
	}
	fmt.Println(id, warnings)
	// Output: c0ffee1234ab []
}

func ExampleClient_ContainerStart() {
	c := exampleClient()
	// Starting an already running container is not an error.
	if err := c.ContainerStart(context.Background(), "c0ffee1234ab"); err != nil {
		panic(err)
	}
	fmt.Println("started")
	// Output: started
}

func ExampleClient_ContainerInspect() {
	c := exampleClient()
	info, err := c.ContainerInspect(context.Background(), "c0ffee1234ab")
	if err != nil {
		panic(err)
	}
	fmt.Println(info.Name, info.State.Status, info.State.Running)
	// Output: /mongotest-example running true
}

func ExampleContainerInspect_HostPort() {
	c := exampleClient()
	info, err := c.ContainerInspect(context.Background(), "c0ffee1234ab")
	if err != nil {
		panic(err)
	}
	// The host port the daemon published the container port on, or "" when
	// that port is not published.
	fmt.Printf("mongodb://127.0.0.1:%s\n", info.HostPort("27017/tcp"))
	fmt.Printf("unpublished: %q\n", info.HostPort("80/tcp"))
	// Output:
	// mongodb://127.0.0.1:34819
	// unpublished: ""
}

func ExampleClient_ContainerTop() {
	c := exampleClient()
	top, err := c.ContainerTop(context.Background(), "c0ffee1234ab")
	if err != nil {
		panic(err)
	}
	fmt.Println(top.Titles)
	for _, row := range top.Processes {
		fmt.Println(strings.Join(row, " "))
	}
	// Output:
	// [PID CMD]
	// 1 mongod --bind_ip_all
}

func ExampleClient_ContainerRemove() {
	c := exampleClient()
	err := c.ContainerRemove(context.Background(), "c0ffee1234ab", dockerapi.RemoveOptions{
		Force:         true, // kill it first if it is still running
		RemoveVolumes: true,
	})
	// Cleanup code can treat "already gone" as success.
	if err != nil && !dockerapi.IsNotFound(err) {
		panic(err)
	}
	fmt.Println("removed")
	// Output: removed
}

func ExampleClient_CopyToContainer() {
	c := exampleClient()
	// This works on a created but not yet started container, so files are in
	// place before the process runs. The archive is streamed, not buffered.
	err := c.CopyToContainer(context.Background(), "c0ffee1234ab", "/etc", []dockerapi.File{
		{Name: "mongo-tls/server.pem", Mode: 0o644, Content: []byte("...certificate and key...")},
		{Name: "mongo-tls/ca.pem", Mode: 0o644, Content: []byte("...ca...")},
	})
	if err != nil {
		panic(err)
	}
	fmt.Println("TLS material is in /etc/mongo-tls")
	// Output: TLS material is in /etc/mongo-tls
}

func ExampleClient_CopyArchiveToContainer() {
	c := exampleClient()
	// Any tar stream works: a file on disk, a pipe, or one built in memory.
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	body := []byte("db.seed.insertOne({ok: 1})\n")
	tw.WriteHeader(&tar.Header{Name: "seed.js", Mode: 0o644, Size: int64(len(body)), Typeflag: tar.TypeReg})
	tw.Write(body)
	tw.Close()

	if err := c.CopyArchiveToContainer(context.Background(), "c0ffee1234ab", "/tmp", &buf); err != nil {
		panic(err)
	}
	fmt.Println("seed.js is in /tmp")
	// Output: seed.js is in /tmp
}

func ExampleClient_Exec() {
	c := exampleClient()
	res, err := c.Exec(context.Background(), "c0ffee1234ab",
		"mongosh", "--quiet", "--eval", "db.runCommand({ping:1}).ok")
	if err != nil {
		panic(err) // a transport or daemon failure
	}
	// A non-zero exit code is reported in the result, not as an error.
	fmt.Printf("exit %d, stdout %q\n", res.ExitCode, res.Stdout)
	// Output: exit 0, stdout "1\n"
}

func ExampleClient_ExecCreate() {
	c := exampleClient()
	ctx := context.Background()
	execID, err := c.ExecCreate(ctx, "c0ffee1234ab", dockerapi.ExecConfig{
		Cmd:        []string{"mongosh", "--quiet", "--eval", "1"},
		Env:        []string{"TERM=dumb"}, // needs API 1.25 or newer
		WorkingDir: "/tmp",                // needs API 1.35 or newer
	})
	if err != nil {
		panic(err)
	}
	fmt.Println("exec instance", execID)
	// Output: exec instance e5ec1d
}

func ExampleClient_ExecStart() {
	c := exampleClient()
	ctx := context.Background()
	execID, err := c.ExecCreate(ctx, "c0ffee1234ab", dockerapi.ExecConfig{Cmd: []string{"mongod", "--version"}})
	if err != nil {
		panic(err)
	}
	stdout, stderr, err := c.ExecStart(ctx, execID)
	if err != nil {
		panic(err)
	}
	fmt.Printf("stdout %q stderr %q\n", stdout, stderr)
	// Output: stdout "1\n" stderr ""
}

func ExampleClient_ExecStartTo() {
	c := exampleClient()
	ctx := context.Background()
	execID, err := c.ExecCreate(ctx, "c0ffee1234ab", dockerapi.ExecConfig{
		Cmd: []string{"sh", "-c", "echo 1"},
	})
	if err != nil {
		panic(err)
	}
	// Output is written as the process produces it, rather than collected
	// in memory: use this for long-running or noisy commands.
	if err := c.ExecStartTo(ctx, execID, os.Stdout, os.Stderr); err != nil {
		panic(err)
	}
	ins, err := c.ExecInspect(ctx, execID)
	if err != nil {
		panic(err)
	}
	fmt.Println("exit code", ins.ExitCode)
	// Output:
	// 1
	// exit code 0
}

func ExampleClient_ExecInspect() {
	c := exampleClient()
	ins, err := c.ExecInspect(context.Background(), "e5ec1d")
	if err != nil {
		panic(err)
	}
	fmt.Println(ins.Running, ins.ExitCode)
	// Output: false 0
}

func ExampleIsNotFound() {
	c := exampleClient()
	_, err := c.ContainerInspect(context.Background(), "no-such-container")
	fmt.Println(dockerapi.IsNotFound(err))
	// Output: true
}

func ExampleStatusError() {
	c := exampleClient()
	err := c.ContainerStart(context.Background(), "no-such-container")
	var se *dockerapi.StatusError
	if errors.As(err, &se) {
		fmt.Println(se.StatusCode, se.Method, se.Path)
		fmt.Println(se.Message)
	}
	// Sentinels work without unwrapping the type.
	fmt.Println(errors.Is(err, dockerapi.ErrNotFound))
	// Output:
	// 404 POST /containers/no-such-container/start
	// No such container: no-such-container
	// true
}

func ExampleStatusError_Hint() {
	// Hint suggests what to do about a status code; Error includes it.
	for _, code := range []int{404, 409, 401} {
		se := &dockerapi.StatusError{StatusCode: code, Message: "...", Method: "GET", Path: "/x"}
		fmt.Println(code, se.Hint())
	}
	// Output:
	// 404 Check the id, name or image tag; the object may have been removed already, or the image was never pulled
	// 409 Another operation on this object is in progress or its name is taken; retry shortly, pick another name, or remove with Force
	// 401 Credentials were refused; log in with `docker login`, or use an image from a public registry
}

func ExampleInvalidArgumentError() {
	c := exampleClient()
	// Rejected client side: no request is sent.
	_, _, err := c.ContainerCreate(context.Background(), "", dockerapi.ContainerConfig{Image: "Mongo:8"})
	var ia *dockerapi.InvalidArgumentError
	if errors.As(err, &ia) {
		fmt.Println("argument:", ia.Argument)
		fmt.Println("value:   ", ia.Value)
		fmt.Println("problem: ", ia.Problem)
	}
	fmt.Println(errors.Is(err, dockerapi.ErrInvalidArgument))
	// Output:
	// argument: image reference
	// value:    Mongo:8
	// problem:  repository names must be lowercase
	// true
}

func ExampleConnectionError() {
	c, err := dockerapi.New(dockerapi.WithHost("unix:///nonexistent/docker.sock"))
	if err != nil {
		panic(err)
	}
	err = c.Negotiate(context.Background())
	var ce *dockerapi.ConnectionError
	if errors.As(err, &ce) {
		fmt.Println("host:   ", ce.Host)
		fmt.Println("problem:", ce.Problem)
	}
	fmt.Println(errors.Is(err, dockerapi.ErrConnectionFailed))
	// Output:
	// host:    unix:///nonexistent/docker.sock
	// problem: the socket does not exist
	// true
}

func ExampleAPIVersionError() {
	// A daemon whose API window starts above what this client supports.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, `{"ApiVersion":"1.70","MinAPIVersion":"1.60"}`)
	}))
	defer srv.Close()
	c, err := dockerapi.New(dockerapi.WithHost("tcp://" + strings.TrimPrefix(srv.URL, "http://")))
	if err != nil {
		panic(err)
	}
	err = c.Negotiate(context.Background())
	var ve *dockerapi.APIVersionError
	if errors.As(err, &ve) {
		fmt.Println("daemon needs at least", ve.ServerMin)
	}
	fmt.Println(errors.Is(err, dockerapi.ErrAPIVersion))
	// Output:
	// daemon needs at least 1.60
	// true
}

func ExamplePullError() {
	c := exampleClient()
	// The daemon reports a failed pull inside a 200 progress stream, so the
	// error comes from the stream rather than the status code.
	err := c.ImagePull(context.Background(), "mongo:nope")
	var pe *dockerapi.PullError
	if errors.As(err, &pe) {
		fmt.Println("reference:", pe.Ref)
		fmt.Println("message:  ", pe.Message)
	}
	fmt.Println(errors.Is(err, dockerapi.ErrPull))
	// Output:
	// reference: mongo:nope
	// message:   manifest for mongo:nope not found: manifest unknown
	// true
}

func ExampleStreamError() {
	c := exampleClient()
	// The daemon can report a problem inside the exec output stream, for
	// example when the container stops mid-command.
	_, _, err := c.ExecStart(context.Background(), "brokenstream")
	var se *dockerapi.StreamError
	if errors.As(err, &se) {
		fmt.Println(se.Problem)
	}
	fmt.Println(errors.Is(err, dockerapi.ErrStream))
	// Output:
	// the daemon reported an error in the exec stream: container is not running
	// true
}

func ExampleResponseError() {
	c := exampleClient()
	// The stub answers this container id with a body that is not valid JSON,
	// which is what a version mismatch tends to look like.
	_, err := c.ContainerInspect(context.Background(), "badjson")
	var re *dockerapi.ResponseError
	if errors.As(err, &re) {
		fmt.Println(re.Method, re.Path, "-", re.Problem)
	}
	fmt.Println(errors.Is(err, dockerapi.ErrDaemonResponse))
	// Output:
	// GET /containers/badjson/json - could not decode the daemon's response
	// true
}

// --- the stub daemon backing the examples above ---------------------------

// exampleDaemon is one fake Docker daemon shared by every example in this
// file, so they run anywhere with no Docker installed and always produce the
// same output. ServeDefaults answers every endpoint with the fixed values
// dockermock documents. Real code has no stub: it calls dockerapi.FromEnv.
var exampleDaemon = sync.OnceValue(func() *dockermock.Daemon {
	d := dockermock.NewDaemon(dockermock.OverTCP())
	d.ServeDefaults()
	return d
})

// stubHost returns the fake daemon's address in DOCKER_HOST form.
func stubHost() string { return exampleDaemon().Host() }

// exampleClient returns a client wired to the fake daemon.
func exampleClient() *dockerapi.Client {
	c, err := dockerapi.New(dockerapi.WithHost(stubHost()))
	if err != nil {
		panic(err)
	}
	return c
}

func ExampleClient_Runtime() {
	// Podman serves the same API, so the endpoints work either way. Which
	// one answered still matters: their version windows differ, and an
	// error that names the wrong product sends the reader nowhere.
	daemon := dockermock.NewDaemon(dockermock.OverTCP())
	defer daemon.Close()
	daemon.ServeDefaults()
	// Answer the way Podman's compatible endpoint does, including the cap
	// at API 1.41 that Podman held through 5.7.
	daemon.ServePodmanVersion("1.41", "5.7.1")

	docker, _ := dockerapi.New(dockerapi.WithHost(daemon.Host()))
	if err := docker.Negotiate(context.Background()); err != nil {
		panic(err)
	}
	fmt.Println("runtime:", docker.Runtime())
	fmt.Println("negotiated:", docker.APIVersion())
	// Output:
	// runtime: podman
	// negotiated: 1.41
}

func ExampleClient_ServerProduct() {
	// The product string is what version errors name, so a Podman user is
	// never told to upgrade a docker daemon they do not have.
	daemon := dockermock.NewDaemon(dockermock.OverTCP())
	defer daemon.Close()
	daemon.ServeDefaults()

	docker, _ := dockerapi.New(dockerapi.WithHost(daemon.Host()))
	if err := docker.Negotiate(context.Background()); err != nil {
		panic(err)
	}
	fmt.Printf("%s reports itself as %q\n", docker.Runtime(), docker.ServerProduct())
	// Output: docker reports itself as "Docker Engine - Community"
}
