package dockermock_test

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/tophergopher/mongotest/dockerapi"
	"github.com/tophergopher/mongotest/dockerclient"
	"github.com/tophergopher/mongotest/dockermock"
)

func ExampleMock() {
	// A Mock records what happened, which is the point when the calls
	// themselves are what the test is about.
	docker := &dockermock.Mock{
		ContainerCreateFunc: func(ctx context.Context, name string, cfg dockerclient.ContainerConfig) (string, []string, error) {
			return "c1", nil, nil
		},
	}
	ctx := context.Background()
	id, _, _ := docker.ContainerCreate(ctx, "mongotest-1", dockerclient.ContainerConfig{Image: "mongo:8"})
	_ = docker.ContainerStart(ctx, id)
	_ = docker.ContainerRemove(ctx, id, dockerclient.RemoveOptions{Force: true})

	fmt.Println(docker.Methods())
	// Output: [ContainerCreate ContainerStart ContainerRemove]
}

func ExampleMock_CallsTo() {
	// A create that reports the image is missing, so the caller pulls once
	// and retries: exactly the sequence worth asserting.
	pulled := false
	docker := &dockermock.Mock{
		ContainerCreateFunc: func(ctx context.Context, name string, cfg dockerclient.ContainerConfig) (string, []string, error) {
			if !pulled {
				return "", nil, &dockerclient.StatusError{
					StatusCode: 404, Message: "No such image: mongo:8",
					Method: "POST", Path: "/containers/create",
				}
			}
			return "c1", nil, nil
		},
		ImagePullFunc: func(ctx context.Context, ref string) error { pulled = true; return nil },
	}

	ctx := context.Background()
	_, _, err := docker.ContainerCreate(ctx, "", dockerclient.ContainerConfig{Image: "mongo:8"})
	if dockerclient.IsNotFound(err) {
		_ = docker.ImagePull(ctx, "mongo:8")
		_, _, err = docker.ContainerCreate(ctx, "", dockerclient.ContainerConfig{Image: "mongo:8"})
	}
	fmt.Println("err:", err)
	fmt.Println("pulls:", len(docker.CallsTo("ImagePull")))
	fmt.Println("creates:", len(docker.CallsTo("ContainerCreate")))
	// Output:
	// err: <nil>
	// pulls: 1
	// creates: 2
}

func ExampleMock_Calls() {
	docker := &dockermock.Mock{}
	_ = docker.ContainerStart(context.Background(), "c1")
	call := docker.Calls()[0]
	fmt.Println(call.Method, call.Args)
	// Output: ContainerStart [c1]
}

func ExampleFake() {
	// A Fake behaves like a daemon, so nothing has to be scripted.
	docker := dockermock.NewFake()
	ctx := context.Background()

	// Creating before pulling fails, just as it would for real.
	_, _, err := docker.ContainerCreate(ctx, "", dockerclient.ContainerConfig{Image: "mongo:8"})
	fmt.Println("before pulling:", dockerclient.IsNotFound(err))

	if err := docker.ImagePull(ctx, "mongo:8"); err != nil {
		panic(err)
	}
	id, _, err := docker.ContainerCreate(ctx, "", dockerclient.ContainerConfig{Image: "mongo:8"})
	if err != nil {
		panic(err)
	}
	info, _ := docker.ContainerInspect(ctx, id)
	fmt.Println("created, running:", info.State.Running)

	_ = docker.ContainerStart(ctx, id)
	info, _ = docker.ContainerInspect(ctx, id)
	fmt.Println("started, running:", info.State.Running)

	_ = docker.ContainerRemove(ctx, id, dockerclient.RemoveOptions{Force: true})
	_, err = docker.ContainerInspect(ctx, id)
	fmt.Println("after removing:", dockerclient.IsNotFound(err))
	// Output:
	// before pulling: true
	// created, running: false
	// started, running: true
	// after removing: true
}

func ExampleNewFake() {
	// WithImages skips the pull when the test is not about pulling.
	docker := dockermock.NewFake(dockermock.WithImages("mongo:8"))
	id, _, err := docker.ContainerCreate(context.Background(), "", dockerclient.ContainerConfig{Image: "mongo:8"})
	fmt.Println(err, strings.HasPrefix(id, "fake"))
	// Output: <nil> true
}

func ExampleFake_Containers() {
	docker := dockermock.NewFake(dockermock.WithImages("mongo:8"))
	ctx := context.Background()
	id, _, _ := docker.ContainerCreate(ctx, "", dockerclient.ContainerConfig{Image: "mongo:8"})

	// Assert nothing was left behind, which is what a cleanup test wants.
	fmt.Println("live:", len(docker.Containers()))
	_ = docker.ContainerRemove(ctx, id, dockerclient.RemoveOptions{Force: true})
	fmt.Println("after cleanup:", len(docker.Containers()))
	// Output:
	// live: 1
	// after cleanup: 0
}

func ExampleFake_exec() {
	// ExecFunc decides what a command produces, so a test can drive code
	// that reacts to output or to a failure.
	docker := dockermock.NewFake(dockermock.WithImages("mongo:8"))
	docker.ExecFunc = func(containerID string, cmd []string) dockerclient.ExecResult {
		if len(cmd) > 0 && cmd[0] == "mongosh" {
			return dockerclient.ExecResult{Stdout: "1\n"}
		}
		return dockerclient.ExecResult{Stderr: "not found\n", ExitCode: 127}
	}
	ctx := context.Background()
	id, _, _ := docker.ContainerCreate(ctx, "", dockerclient.ContainerConfig{Image: "mongo:8"})

	ok, _ := docker.Exec(ctx, id, "mongosh", "--quiet", "--eval", "1")
	fmt.Printf("%q %d\n", ok.Stdout, ok.ExitCode)
	bad, _ := docker.Exec(ctx, id, "nope")
	fmt.Printf("%q %d\n", bad.Stderr, bad.ExitCode)
	// Output:
	// "1\n" 0
	// "not found\n" 127
}

func ExampleFake_files() {
	// Everything copied in is kept, so a test can assert what was placed
	// where without a real container.
	docker := dockermock.NewFake(dockermock.WithImages("mongo:8"))
	ctx := context.Background()
	id, _, _ := docker.ContainerCreate(ctx, "", dockerclient.ContainerConfig{Image: "mongo:8"})
	_ = docker.CopyToContainer(ctx, id, "/etc", []dockerclient.File{
		{Name: "mongo-tls/ca.pem", Mode: 0o644, Content: []byte("ca")},
	})
	fmt.Printf("%s\n", docker.Containers()[0].Files["/etc/mongo-tls/ca.pem"])
	// Output: ca
}

func ExampleNewDaemon() {
	// A Daemon speaks HTTP, so a real client can be pointed at it. Use it
	// when the wire conversation is the subject.
	d := dockermock.NewDaemon()
	defer d.Close()
	d.ServeDefaults()

	c, err := dockerapi.New(dockerapi.WithHost(d.Host()))
	if err != nil {
		panic(err)
	}
	info, err := c.ContainerInspect(context.Background(), dockermock.DefaultContainerID)
	if err != nil {
		panic(err)
	}
	fmt.Println(info.Name, info.HostPort("27017/tcp"))
	// Output: /mongotest-example 34819
}

func ExampleDaemon_Requests() {
	d := dockermock.NewDaemon()
	defer d.Close()
	d.ServeDefaults()
	c, _ := dockerapi.New(dockerapi.WithHost(d.Host()))
	_ = c.ContainerStart(context.Background(), "abc")

	// The first request is the version negotiation every client does once.
	for _, r := range d.Requests() {
		fmt.Println(r.Method, r.Path)
	}
	// Output:
	// GET /version
	// POST /containers/abc/start
}

func ExampleDaemon_ServeFake() {
	// ServeFake puts a stateful store behind the HTTP routes, so a real
	// client sees a daemon that actually remembers what it did.
	d := dockermock.NewDaemon()
	defer d.Close()
	d.ServeFake(dockermock.NewFake())

	c, _ := dockerapi.New(dockerapi.WithHost(d.Host()))
	ctx := context.Background()
	if err := c.ImagePull(ctx, "mongo:8"); err != nil {
		panic(err)
	}
	id, _, err := c.ContainerCreate(ctx, "", dockerclient.ContainerConfig{Image: "mongo:8"})
	if err != nil {
		panic(err)
	}
	_ = c.ContainerStart(ctx, id)
	info, _ := c.ContainerInspect(ctx, id)
	fmt.Println("running:", info.State.Running)

	_ = c.ContainerRemove(ctx, id, dockerclient.RemoveOptions{Force: true})
	_, err = c.ContainerInspect(ctx, id)
	fmt.Println("gone:", dockerclient.IsNotFound(err))
	// Output:
	// running: true
	// gone: true
}

func ExampleDaemon_Handle() {
	// A route of your own, for the case a test needs a specific answer.
	d := dockermock.NewDaemon()
	defer d.Close()
	d.ServeVersion(dockermock.DefaultAPIVersion, dockermock.DefaultMinAPIVersion)
	d.Handle("GET", "/containers/{id}/json", func(w http.ResponseWriter, r *http.Request) {
		dockermock.Error(w, 500, "the daemon fell over inspecting "+dockermock.PathParam(r, "id"))
	})

	c, _ := dockerapi.New(dockerapi.WithHost(d.Host()))
	_, err := c.ContainerInspect(context.Background(), "abc")
	var se *dockerclient.StatusError
	if errors.As(err, &se) {
		fmt.Println(se.StatusCode, se.Message)
	}
	// Output: 500 the daemon fell over inspecting abc
}

func ExampleOverTCP() {
	// A TCP daemon, for exercising that transport or on a platform without
	// unix sockets.
	d := dockermock.NewDaemon(dockermock.OverTCP())
	defer d.Close()
	fmt.Println(strings.HasPrefix(d.Host(), "tcp://"))
	// Output: true
}

func ExampleFrame() {
	// Exec output is framed so stdout and stderr share one connection.
	frame := dockermock.Frame(dockermock.StreamStdout, "hi")
	fmt.Println(frame[0], frame[7], string(frame[8:]))
	// Output: 1 2 hi
}

func ExampleDaemon_ServeVersion() {
	// An old daemon, to check a client refuses to talk to it.
	d := dockermock.NewDaemon()
	defer d.Close()
	d.ServeVersion("1.20", "1.12")

	c, _ := dockerapi.New(dockerapi.WithHost(d.Host()))
	err := c.Negotiate(context.Background())
	fmt.Println(errors.Is(err, dockerclient.ErrAPIVersion))
	// Output: true
}

func ExampleJSON() {
	// JSON is what a custom route uses to answer the way the daemon does.
	daemon := dockermock.NewDaemon(dockermock.OverTCP())
	defer daemon.Close()
	daemon.ServeVersion(dockermock.DefaultAPIVersion, dockermock.DefaultMinAPIVersion)
	daemon.Handle(http.MethodGet, "/containers/{id}/json", func(w http.ResponseWriter, r *http.Request) {
		dockermock.JSON(w, http.StatusOK, dockerclient.ContainerInspect{
			ID:    dockermock.PathParam(r, "id"),
			State: dockerclient.ContainerState{Running: true},
		})
	})

	docker, _ := dockerapi.New(dockerapi.WithHost(daemon.Host()))
	info, err := docker.ContainerInspect(context.Background(), "c0ffee1234ab")
	fmt.Println(info.ID, info.State.Running, err)
	// Output: c0ffee1234ab true <nil>
}

func ExampleError() {
	// Error writes the daemon's own error shape, {"message": ...}, so the
	// client turns it into the same typed error a real daemon would produce.
	daemon := dockermock.NewDaemon(dockermock.OverTCP())
	defer daemon.Close()
	daemon.ServeVersion(dockermock.DefaultAPIVersion, dockermock.DefaultMinAPIVersion)
	daemon.Handle(http.MethodGet, "/containers/{id}/json", func(w http.ResponseWriter, r *http.Request) {
		dockermock.Error(w, http.StatusNotFound, "No such container: "+dockermock.PathParam(r, "id"))
	})

	docker, _ := dockerapi.New(dockerapi.WithHost(daemon.Host()))
	_, err := docker.ContainerInspect(context.Background(), "missing")
	fmt.Println(dockerclient.IsNotFound(err))
	fmt.Println(err)
	// Output:
	// true
	// docker: the docker daemon rejected GET /containers/missing/json with status 404: No such container: missing. Check the id, name or image tag; the object may have been removed already, or the image was never pulled
}

func ExamplePathParam() {
	// A route pattern can capture a segment; PathParam reads it back out.
	daemon := dockermock.NewDaemon(dockermock.OverTCP())
	defer daemon.Close()
	daemon.ServeVersion(dockermock.DefaultAPIVersion, dockermock.DefaultMinAPIVersion)
	daemon.Handle(http.MethodDelete, "/containers/{id}", func(w http.ResponseWriter, r *http.Request) {
		fmt.Println("asked to remove", dockermock.PathParam(r, "id"))
		w.WriteHeader(http.StatusNoContent)
	})

	docker, _ := dockerapi.New(dockerapi.WithHost(daemon.Host()))
	err := docker.ContainerRemove(context.Background(), "c0ffee1234ab", dockerclient.RemoveOptions{Force: true})
	fmt.Println("error:", err)
	// Output:
	// asked to remove c0ffee1234ab
	// error: <nil>
}

func ExampleHijack() {
	// Exec start cannot go through a normal ResponseWriter: the daemon keeps
	// the connection and writes framed bytes down it. Hijack does that, and
	// Frame builds the frames.
	daemon := dockermock.NewDaemon(dockermock.OverTCP())
	defer daemon.Close()
	daemon.ServeVersion(dockermock.DefaultAPIVersion, dockermock.DefaultMinAPIVersion)
	daemon.Handle(http.MethodPost, "/containers/{id}/exec", func(w http.ResponseWriter, r *http.Request) {
		dockermock.JSON(w, http.StatusCreated, map[string]string{"Id": "e5ec1d"})
	})
	daemon.Handle(http.MethodPost, "/exec/{id}/start", func(w http.ResponseWriter, r *http.Request) {
		stream := append(dockermock.Frame(dockermock.StreamStdout, "counted 3 documents\n"),
			dockermock.Frame(dockermock.StreamStderr, "warning: no index\n")...)
		dockermock.Hijack(w, stream)
	})
	daemon.Handle(http.MethodGet, "/exec/{id}/json", func(w http.ResponseWriter, r *http.Request) {
		dockermock.JSON(w, http.StatusOK, map[string]any{"Running": false, "ExitCode": 0})
	})

	docker, _ := dockerapi.New(dockerapi.WithHost(daemon.Host()))
	res, err := docker.Exec(context.Background(), "c0ffee1234ab",
		"mongosh", "--eval", "db.things.countDocuments({})")
	fmt.Printf("exit %d\nstdout: %sstderr: %serror: %v\n", res.ExitCode, res.Stdout, res.Stderr, err)
	// Output:
	// exit 0
	// stdout: counted 3 documents
	// stderr: warning: no index
	// error: <nil>
}

func ExampleWithImages() {
	// A Fake refuses to create a container from an image that was never
	// pulled, the same as the daemon. WithImages skips the pull when the pull
	// is not what the test is about.
	ctx := context.Background()

	bare := dockermock.NewFake()
	_, _, err := bare.ContainerCreate(ctx, "", dockerclient.ContainerConfig{Image: "mongo:8"})
	fmt.Println("without the image:", dockerclient.IsNotFound(err))

	docker := dockermock.NewFake(dockermock.WithImages("mongo:8"))
	id, _, err := docker.ContainerCreate(ctx, "", dockerclient.ContainerConfig{Image: "mongo:8"})
	fmt.Println("with the image:", id != "", err)
	// Output:
	// without the image: true
	// with the image: true <nil>
}

func ExampleDaemon_ServeDefaults() {
	// ServeDefaults answers every endpoint the Client interface covers with
	// fixed values, so a whole lifecycle succeeds and prints the same thing
	// every run. It is what the examples in this repository are built on.
	daemon := dockermock.NewDaemon(dockermock.OverTCP())
	defer daemon.Close()
	daemon.ServeDefaults()

	docker, _ := dockerapi.New(dockerapi.WithHost(daemon.Host()))
	ctx := context.Background()
	id, _, _ := docker.ContainerCreate(ctx, "mongotest-example", dockerclient.ContainerConfig{Image: "mongo:8"})
	_ = docker.ContainerStart(ctx, id)
	info, _ := docker.ContainerInspect(ctx, id)
	fmt.Println(id, info.Name, info.HostPort("27017/tcp"))

	// Two ids behave specially, so failures can be demonstrated too.
	_, err := docker.ContainerInspect(ctx, "no-such-container")
	fmt.Println("missing:", dockerclient.IsNotFound(err))
	// Output:
	// c0ffee1234ab /mongotest-example 34819
	// missing: true
}

func ExampleDaemon_Reset() {
	// Reset clears the recorded requests but keeps the routes, which is what
	// a subtest wants between cases.
	daemon := dockermock.NewDaemon(dockermock.OverTCP())
	defer daemon.Close()
	daemon.ServeDefaults()

	docker, _ := dockerapi.New(dockerapi.WithHost(daemon.Host()))
	_, _ = docker.ContainerInspect(context.Background(), "c0ffee1234ab")
	fmt.Println("before reset:", len(daemon.Requests()) > 0)

	daemon.Reset()
	fmt.Println("after reset:", len(daemon.Requests()))

	// The routes survive, so the next call still works.
	info, err := docker.ContainerInspect(context.Background(), "c0ffee1234ab")
	fmt.Println("still routed:", info.ID, err)
	// Output:
	// before reset: true
	// after reset: 0
	// still routed: c0ffee1234ab <nil>
}

func ExampleMock_Reset() {
	// Reset forgets the recorded calls and leaves the stub functions in place.
	docker := &dockermock.Mock{}
	ctx := context.Background()
	_ = docker.ContainerStart(ctx, "c1")
	fmt.Println(docker.Methods())

	docker.Reset()
	_ = docker.ContainerRemove(ctx, "c1", dockerclient.RemoveOptions{})
	fmt.Println(docker.Methods())
	// Output:
	// [ContainerStart]
	// [ContainerRemove]
}

func ExampleMock_Methods() {
	// Methods gives the whole sequence in one value, which is usually the
	// clearest thing to assert on.
	docker := &dockermock.Mock{}
	ctx := context.Background()
	_ = docker.ImagePull(ctx, "mongo:8")
	id, _, _ := docker.ContainerCreate(ctx, "", dockerclient.ContainerConfig{Image: "mongo:8"})
	_ = docker.ContainerStart(ctx, id)
	_, _ = docker.ContainerInspect(ctx, id)
	_ = docker.ContainerRemove(ctx, id, dockerclient.RemoveOptions{Force: true})

	fmt.Println(docker.Methods())
	// Output: [ImagePull ContainerCreate ContainerStart ContainerInspect ContainerRemove]
}

func ExampleDaemon_Host() {
	// Host is in DOCKER_HOST form and goes straight into a client's WithHost
	// option. URL is the same server as a plain http base, for the odd test
	// that wants to issue its own request.
	daemon := dockermock.NewDaemon(dockermock.OverTCP())
	defer daemon.Close()
	daemon.ServeDefaults()

	fmt.Println("host scheme:", strings.SplitN(daemon.Host(), "://", 2)[0])
	fmt.Println("url scheme:", strings.SplitN(daemon.URL(), "://", 2)[0])

	docker, err := dockerapi.New(dockerapi.WithHost(daemon.Host()))
	fmt.Println("client built:", err == nil)
	_, err = docker.ContainerInspect(context.Background(), dockermock.DefaultContainerID)
	fmt.Println("and it reaches the daemon:", err == nil)
	// Output:
	// host scheme: tcp
	// url scheme: http
	// client built: true
	// and it reaches the daemon: true
}

func ExampleDaemon_Close() {
	// Close shuts the server down. In a test this belongs in t.Cleanup rather
	// than a defer, so it still runs when a subtest fails.
	daemon := dockermock.NewDaemon(dockermock.OverTCP())
	daemon.ServeDefaults()

	docker, _ := dockerapi.New(dockerapi.WithHost(daemon.Host()))
	_, err := docker.ContainerInspect(context.Background(), dockermock.DefaultContainerID)
	fmt.Println("while open:", err == nil)

	daemon.Close()
	_, err = docker.ContainerInspect(context.Background(), dockermock.DefaultContainerID)
	fmt.Println("after close:", errors.Is(err, dockerclient.ErrConnectionFailed))
	// Output:
	// while open: true
	// after close: true
}

func ExampleMock_Host() {
	// A Mock dials nothing, so Host is a placeholder. It exists only because
	// the interface has it, and it is a useful thing to see in a log line
	// when a test is running against a double rather than a daemon.
	docker := &dockermock.Mock{}
	fmt.Println(docker.Host())
	fmt.Println(dockermock.NewFake().Host())
	// Output:
	// mock://dockermock
	// fake://dockermock
}
