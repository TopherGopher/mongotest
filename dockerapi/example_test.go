package dockerapi_test

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"log"
	"log/slog"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/tophergopher/mongotest/dockerapi"
)

// The examples in this file need a running Docker daemon, so they carry no
// "Output:" comment and are compiled but not executed by "go test". They
// show the intended calling pattern for each exported function.

func ExampleFromEnv() {
	// Locate the daemon like the docker CLI: DOCKER_HOST, then the current
	// docker context, then the default socket.
	c, err := dockerapi.FromEnv()
	if err != nil {
		log.Fatal(err) // the message says which variable or setting to fix
	}
	fmt.Println("talking to", c.Host())
}

func ExampleNew() {
	c, err := dockerapi.New(
		dockerapi.WithHost("tcp://127.0.0.1:2375"),
		dockerapi.WithUserAgent("my-tests/1.0"),
		dockerapi.WithLogger(slog.Default()),
	)
	if err != nil {
		log.Fatal(err)
	}
	fmt.Println(c.Host())
}

func ExampleWithTLSConfig() {
	caPEM, err := os.ReadFile("/etc/docker/certs/ca.pem")
	if err != nil {
		log.Fatal(err)
	}
	pool := x509.NewCertPool()
	pool.AppendCertsFromPEM(caPEM)
	c, err := dockerapi.New(
		dockerapi.WithHost("tcp://build-host:2376"),
		dockerapi.WithTLSConfig(&tls.Config{RootCAs: pool}),
	)
	if err != nil {
		log.Fatal(err)
	}
	_ = c
}

func ExampleWithHTTPClient() {
	// Answer every request in-process: handy for unit tests that must not
	// touch a real daemon.
	rt := roundTripperFunc(func(req *http.Request) (*http.Response, error) {
		body := `{"ApiVersion":"1.44","MinAPIVersion":"1.24"}`
		return &http.Response{
			StatusCode: 200,
			Header:     http.Header{"Content-Type": {"application/json"}},
			Body:       http.NoBody,
			Request:    req,
			// A real stub would return body here; kept short for the example.
			ContentLength: int64(len(body)),
		}, nil
	})
	c, err := dockerapi.New(
		dockerapi.WithHost("unix:///stub/docker.sock"),
		dockerapi.WithHTTPClient(&http.Client{Transport: rt}),
	)
	if err != nil {
		log.Fatal(err)
	}
	_ = c
}

type roundTripperFunc func(*http.Request) (*http.Response, error)

func (f roundTripperFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func ExampleWithAPIVersion() {
	// Pin the API version instead of negotiating; DOCKER_API_VERSION has
	// the same effect.
	c, err := dockerapi.New(dockerapi.WithAPIVersion("1.43"))
	if err != nil {
		log.Fatal(err)
	}
	fmt.Println(c.APIVersion()) // "1.43" before any request
}

func ExampleClient_Negotiate() {
	c, err := dockerapi.FromEnv()
	if err != nil {
		log.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	// Negotiation is implicit on the first request; call it directly to
	// check connectivity up front.
	if err := c.Negotiate(ctx); err != nil {
		var ce *dockerapi.ConnectionError
		if errors.As(err, &ce) {
			log.Fatalf("no daemon at %s: %s", ce.Host, ce.Fix)
		}
		log.Fatal(err)
	}
	fmt.Println("using API", c.APIVersion())
}

func ExampleClient_ImagePull() {
	c, _ := dockerapi.FromEnv()
	ctx := context.Background()
	if err := c.ImagePull(ctx, "mongo:8"); err != nil {
		if errors.Is(err, dockerapi.ErrUnauthorized) {
			log.Fatal("this registry needs credentials: run docker login")
		}
		log.Fatal(err)
	}
}

func ExampleClient_ImageInspect() {
	c, _ := dockerapi.FromEnv()
	ctx := context.Background()
	img, err := c.ImageInspect(ctx, "mongo:8")
	if dockerapi.IsNotFound(err) {
		fmt.Println("mongo:8 is not pulled yet")
		return
	}
	if err != nil {
		log.Fatal(err)
	}
	fmt.Println(img.ID, img.RepoTags, img.Architecture)
}

func ExampleClient_ContainerCreate() {
	c, _ := dockerapi.FromEnv()
	ctx := context.Background()
	cfg := dockerapi.ContainerConfig{
		Image:        "mongo:8",
		Cmd:          []string{"--replSet", "rs0"}, // the image entrypoint prepends mongod
		Labels:       map[string]string{"mongotest": "regression"},
		ExposedPorts: map[string]struct{}{"27017/tcp": {}},
		HostConfig: &dockerapi.HostConfig{PortBindings: map[string][]dockerapi.PortBinding{
			"27017/tcp": {{HostIP: "127.0.0.1", HostPort: ""}}, // "" lets the daemon choose a free port
		}},
	}
	id, warnings, err := c.ContainerCreate(ctx, "mongotest-example", cfg)
	if dockerapi.IsNotFound(err) {
		// The image is not local yet: pull once and retry.
		if err = c.ImagePull(ctx, cfg.Image); err != nil {
			log.Fatal(err)
		}
		id, warnings, err = c.ContainerCreate(ctx, "mongotest-example", cfg)
	}
	if err != nil {
		log.Fatal(err)
	}
	fmt.Println(id, warnings)
}

func ExampleClient_ContainerStart() {
	c, _ := dockerapi.FromEnv()
	ctx := context.Background()
	if err := c.ContainerStart(ctx, "mongotest-example"); err != nil {
		log.Fatal(err) // a second start of a running container is not an error
	}
}

func ExampleClient_ContainerInspect() {
	c, _ := dockerapi.FromEnv()
	ctx := context.Background()
	info, err := c.ContainerInspect(ctx, "mongotest-example")
	if err != nil {
		log.Fatal(err)
	}
	fmt.Println(info.State.Running, info.HostPort("27017/tcp"))
}

func ExampleClient_ContainerTop() {
	c, _ := dockerapi.FromEnv()
	ctx := context.Background()
	top, err := c.ContainerTop(ctx, "mongotest-example")
	if err != nil {
		log.Fatal(err)
	}
	for _, row := range top.Processes {
		fmt.Println(strings.Join(row, " "))
	}
}

func ExampleClient_ContainerRemove() {
	c, _ := dockerapi.FromEnv()
	ctx := context.Background()
	err := c.ContainerRemove(ctx, "mongotest-example", dockerapi.RemoveOptions{Force: true, RemoveVolumes: true})
	if err != nil && !dockerapi.IsNotFound(err) {
		log.Fatal(err) // already gone is fine for cleanup code
	}
}

func ExampleClient_CopyToContainer() {
	c, _ := dockerapi.FromEnv()
	ctx := context.Background()
	// Works on a created-but-not-started container, so files are in place
	// before the process starts.
	err := c.CopyToContainer(ctx, "mongotest-example", "/etc", []dockerapi.File{
		{Name: "mongo-tls/server.pem", Mode: 0o644, Content: []byte("...cert and key...")},
		{Name: "mongo-tls/ca.pem", Mode: 0o644, Content: []byte("...ca...")},
	})
	if err != nil {
		log.Fatal(err)
	}
}

func ExampleClient_CopyArchiveToContainer() {
	c, _ := dockerapi.FromEnv()
	ctx := context.Background()
	f, err := os.Open("fixtures/seed-data.tar")
	if err != nil {
		log.Fatal(err)
	}
	defer f.Close()
	// Any tar stream is uploaded as is and extracted under the destination.
	if err := c.CopyArchiveToContainer(ctx, "mongotest-example", "/seed", f); err != nil {
		log.Fatal(err)
	}
}

func ExampleClient_Exec() {
	c, _ := dockerapi.FromEnv()
	ctx := context.Background()
	res, err := c.Exec(ctx, "mongotest-example", "mongosh", "--quiet", "--eval", "db.runCommand({ping:1}).ok")
	if err != nil {
		log.Fatal(err) // transport or daemon failure
	}
	if res.ExitCode != 0 {
		log.Fatalf("mongosh exited %d: %s", res.ExitCode, res.Stderr) // the command itself failed
	}
	fmt.Print(res.Stdout) // "1\n"
}

func ExampleClient_ExecStartTo() {
	c, _ := dockerapi.FromEnv()
	ctx := context.Background()
	// Stream a long-running command's output instead of buffering it.
	execID, err := c.ExecCreate(ctx, "mongotest-example", dockerapi.ExecConfig{
		Cmd:        []string{"sh", "-c", "for i in 1 2 3; do echo $i; sleep 1; done"},
		Env:        []string{"TERM=dumb"},
		WorkingDir: "/tmp",
	})
	if err != nil {
		log.Fatal(err)
	}
	if err := c.ExecStartTo(ctx, execID, os.Stdout, os.Stderr); err != nil {
		log.Fatal(err)
	}
	ins, err := c.ExecInspect(ctx, execID)
	if err != nil {
		log.Fatal(err)
	}
	fmt.Println("exit code", ins.ExitCode)
}

func ExampleClient_ExecCreate() {
	c, _ := dockerapi.FromEnv()
	ctx := context.Background()
	execID, err := c.ExecCreate(ctx, "mongotest-example", dockerapi.ExecConfig{Cmd: []string{"mongod", "--version"}})
	if err != nil {
		log.Fatal(err)
	}
	stdout, stderr, err := c.ExecStart(ctx, execID)
	if err != nil {
		log.Fatal(err)
	}
	fmt.Printf("%s%s", stdout, stderr)
}

func ExampleClient_ExecInspect() {
	c, _ := dockerapi.FromEnv()
	ctx := context.Background()
	ins, err := c.ExecInspect(ctx, "0123456789abcdef")
	if err != nil {
		log.Fatal(err)
	}
	fmt.Println(ins.Running, ins.ExitCode)
}

func ExampleIsNotFound() {
	c, _ := dockerapi.FromEnv()
	_, err := c.ContainerInspect(context.Background(), "no-such-container")
	if dockerapi.IsNotFound(err) {
		fmt.Println("gone already")
	}
}

func ExampleStatusError() {
	c, _ := dockerapi.FromEnv()
	err := c.ContainerStart(context.Background(), "no-such-container")
	var se *dockerapi.StatusError
	if errors.As(err, &se) {
		fmt.Println(se.StatusCode, se.Message, se.Hint())
	}
}

func ExampleInvalidArgumentError() {
	c, _ := dockerapi.FromEnv()
	_, _, err := c.ContainerCreate(context.Background(), "", dockerapi.ContainerConfig{Image: "Mongo:8"})
	var ia *dockerapi.InvalidArgumentError
	if errors.As(err, &ia) {
		fmt.Println(ia.Argument, ia.Problem, ia.Fix)
	}
	// Sentinels work too:
	fmt.Println(errors.Is(err, dockerapi.ErrInvalidArgument))
}
