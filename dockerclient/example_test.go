package dockerclient_test

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"

	"github.com/tophergopher/mongotest/dockerclient"
	"github.com/tophergopher/mongotest/dockermock"
)

// startMongo is the kind of code this package exists for: it works against
// any client, so a caller decides whether that is dockerapi, mobyclient or a
// double from dockermock.
func startMongo(ctx context.Context, docker dockerclient.Client) (string, error) {
	cfg := dockerclient.ContainerConfig{
		Image:        "mongo:8",
		Labels:       map[string]string{"mongotest": "regression"},
		ExposedPorts: map[string]struct{}{"27017/tcp": {}},
		HostConfig: &dockerclient.HostConfig{PortBindings: map[string][]dockerclient.PortBinding{
			"27017/tcp": {{HostIP: "127.0.0.1", HostPort: ""}}, // the daemon picks a free port
		}},
	}
	id, _, err := docker.ContainerCreate(ctx, "", cfg)
	if dockerclient.IsNotFound(err) {
		// The image is not local yet: pull once, then retry.
		if err = docker.ImagePull(ctx, cfg.Image); err != nil {
			return "", err
		}
		id, _, err = docker.ContainerCreate(ctx, "", cfg)
	}
	if err != nil {
		return "", err
	}
	return id, docker.ContainerStart(ctx, id)
}

func ExampleClient() {
	// A Fake stands in for a daemon, so this runs with no Docker installed.
	docker := dockermock.NewFake()
	ctx := context.Background()

	id, err := startMongo(ctx, docker)
	if err != nil {
		fmt.Println("could not start mongo:", err)
		return
	}
	info, err := docker.ContainerInspect(ctx, id)
	if err != nil {
		fmt.Println(err)
		return
	}
	fmt.Println("running:", info.State.Running)
	fmt.Println("published:", info.HostPort("27017/tcp") != "")
	// Output:
	// running: true
	// published: true
}

func ExampleContainerInspect_HostPort() {
	info := dockerclient.ContainerInspect{
		NetworkSettings: dockerclient.NetworkSettings{Ports: map[string][]dockerclient.PortBinding{
			"27017/tcp": {{HostIP: "127.0.0.1", HostPort: "34819"}},
		}},
	}
	// The port the daemon chose, which is how a caller learns where to
	// connect after leaving HostPort empty.
	fmt.Printf("mongodb://127.0.0.1:%s\n", info.HostPort("27017/tcp"))
	fmt.Printf("unpublished: %q\n", info.HostPort("80/tcp"))
	// Output:
	// mongodb://127.0.0.1:34819
	// unpublished: ""
}

func ExampleIsNotFound() {
	docker := dockermock.NewFake()
	_, err := docker.ContainerInspect(context.Background(), "never-created")
	fmt.Println(dockerclient.IsNotFound(err))
	// Output: true
}

func ExampleNopLogger() {
	// The default for every client: it discards everything.
	logger := dockerclient.NopLogger()
	logger.Info("this goes nowhere", "key", "value")
	fmt.Println("nothing was printed")
	// Output: nothing was printed
}

func ExampleInvalidArgument() {
	// Implementations build the same rejection, so the message a caller sees
	// does not depend on which client produced it.
	err := dockerclient.InvalidArgument("image reference", "Mongo:8",
		"repository names must be lowercase",
		`use the form [registry[:port]/]repository[:tag|@digest]`)
	fmt.Println(err)
	fmt.Println(errors.Is(err, dockerclient.ErrInvalidArgument))
	// Output:
	// docker: invalid image reference "Mongo:8": repository names must be lowercase. use the form [registry[:port]/]repository[:tag|@digest]
	// true
}

func ExampleParseImageRef() {
	for _, ref := range []string{"mongo", "mongo:8", "localhost:5000/team/mongo:8.0"} {
		r, err := dockerclient.ParseImageRef(ref)
		if err != nil {
			fmt.Println(err)
			continue
		}
		fmt.Printf("%-30s name=%s tag=%s\n", ref, r.Name, r.Tag)
	}
	_, err := dockerclient.ParseImageRef("Mongo:8")
	fmt.Println(errors.Is(err, dockerclient.ErrInvalidArgument))
	// Output:
	// mongo                          name=mongo tag=latest
	// mongo:8                        name=mongo tag=8
	// localhost:5000/team/mongo:8.0  name=localhost:5000/team/mongo tag=8.0
	// true
}

func ExampleCheckID() {
	// Ids go straight into a request path, so anything that could change the
	// path is refused.
	id, err := dockerclient.CheckID("container", "  /mongotest-1 ")
	fmt.Printf("%q %v\n", id, err)
	_, err = dockerclient.CheckID("container", "a/b")
	fmt.Println(errors.Is(err, dockerclient.ErrInvalidArgument))
	// Output:
	// "mongotest-1" <nil>
	// true
}

func ExampleCheckContainerName() {
	name, err := dockerclient.CheckContainerName("mongotest-1a2b")
	fmt.Printf("%q %v\n", name, err)
	// An empty name is allowed: the daemon generates one.
	name, err = dockerclient.CheckContainerName("")
	fmt.Printf("%q %v\n", name, err)
	// Output:
	// "mongotest-1a2b" <nil>
	// "" <nil>
}

func ExampleCheckImageRefOrID() {
	// Inspect accepts either form.
	for _, in := range []string{"mongo:8", "sha256:41c3b7abb48e"} {
		got, err := dockerclient.CheckImageRefOrID(in)
		fmt.Printf("%q %v\n", got, err)
	}
	// Output:
	// "mongo:8" <nil>
	// "sha256:41c3b7abb48e" <nil>
}

func ExampleCheckPortKey() {
	fmt.Println(dockerclient.CheckPortKey("ExposedPorts", "27017/tcp"))
	fmt.Println(errors.Is(dockerclient.CheckPortKey("ExposedPorts", "70000/tcp"), dockerclient.ErrInvalidArgument))
	// Output:
	// <nil>
	// true
}

func ExampleCheckHostPort() {
	// Empty means the daemon assigns one; a range is allowed.
	fmt.Println(dockerclient.CheckHostPort("PortBindings", ""))
	fmt.Println(dockerclient.CheckHostPort("PortBindings", "40000-40010"))
	fmt.Println(errors.Is(dockerclient.CheckHostPort("PortBindings", "40010-40000"), dockerclient.ErrInvalidArgument))
	// Output:
	// <nil>
	// <nil>
	// true
}

func ExampleValidateFiles() {
	files := []dockerclient.File{{Name: "mongo-tls/ca.pem", Mode: 0o644, Content: []byte("ca")}}
	fmt.Println(dockerclient.ValidateFiles(files))
	// A name that would escape the destination is refused before any upload
	// starts.
	fmt.Println(errors.Is(dockerclient.ValidateFiles([]dockerclient.File{{Name: "../escape"}}), dockerclient.ErrInvalidArgument))
	// So is a mode carrying anything but permission bits.
	fmt.Println(errors.Is(dockerclient.ValidateFiles([]dockerclient.File{{Name: "x", Mode: fs.ModeSetuid | 0o755}}), dockerclient.ErrInvalidArgument))
	// Output:
	// <nil>
	// true
	// true
}

func ExampleContainerConfig_Validate() {
	cfg := dockerclient.ContainerConfig{
		Image:        "mongo:8",
		ExposedPorts: map[string]struct{}{"27017/tcp": {}},
	}
	fmt.Println(cfg.Validate())
	fmt.Println(dockerclient.ContainerConfig{}.Validate())
	// Output:
	// <nil>
	// docker: invalid ContainerConfig.Image: the image is required. set Image to a reference such as "mongo:8"
}

func ExampleExecConfig_Validate() {
	cfg := dockerclient.ExecConfig{Cmd: []string{"mongosh", "--quiet"}, WorkingDir: "/tmp"}
	// WorkingDir needs API 1.35, so the same config is fine on a new daemon
	// and refused on an old one.
	fmt.Println(cfg.Validate("1.44"))
	fmt.Println(errors.Is(cfg.Validate("1.30"), dockerclient.ErrAPIVersion))
	// Output:
	// <nil>
	// true
}

func ExampleStatusError() {
	err := &dockerclient.StatusError{
		StatusCode: 404, Message: "No such container: abc",
		Method: "GET", Path: "/containers/abc/json",
	}
	fmt.Println(errors.Is(err, dockerclient.ErrNotFound))
	fmt.Println(err.Hint())
	// Output:
	// true
	// Check the id, name or image tag; the object may have been removed already, or the image was never pulled
}

func ExampleWrapConnectionError() {
	// Transport failures are turned into the same actionable error by every
	// implementation.
	err := dockerclient.WrapConnectionError("unix:///var/run/docker.sock", fs.ErrNotExist)
	fmt.Println(errors.Is(err, dockerclient.ErrConnectionFailed))
	var ce *dockerclient.ConnectionError
	if errors.As(err, &ce) {
		fmt.Println(ce.Problem)
	}
	// Output:
	// true
	// the socket does not exist
}

func ExampleConnectionFailed() {
	err := dockerclient.ConnectionFailed("tcp://build:2375", "connection refused",
		"Is the docker daemon running?", nil)
	fmt.Println(err)
	// Output: docker: cannot connect to the docker daemon at tcp://build:2375: connection refused. Is the docker daemon running?
}

func ExampleDecodeError() {
	err := dockerclient.DecodeError("GET", "/containers/abc/json", errors.New("unexpected EOF"))
	fmt.Println(errors.Is(err, dockerclient.ErrDaemonResponse))
	// Output: true
}

func ExampleUnexpectedStatus() {
	err := dockerclient.UnexpectedStatus("POST", "/containers/create", 202)
	fmt.Println(errors.Is(err, dockerclient.ErrDaemonResponse))
	// Output: true
}

func ExampleRootCause() {
	inner := errors.New("connection refused")
	wrapped := fmt.Errorf("dialing: %w", fmt.Errorf("transport: %w", inner))
	fmt.Println(dockerclient.RootCause(wrapped))
	// Output: connection refused
}

func ExampleAPIVersionError() {
	// One feature needed a newer API than the daemon negotiated. The message
	// names the feature so the caller knows which option to drop.
	err := &dockerclient.APIVersionError{
		Feature:    "exec working directory",
		Required:   "1.35",
		Negotiated: "1.24",
	}
	fmt.Println(err)
	fmt.Println(errors.Is(err, dockerclient.ErrAPIVersion))
	// Output:
	// docker: exec working directory requires docker API 1.35 or newer but the daemon negotiated 1.24. Upgrade the docker daemon, or drop the exec working directory option
	// true
}

func ExampleAPIVersionError_client() {
	// The daemon is newer than the client. The fix is on this side, so the
	// message says to upgrade mongotest rather than docker.
	err := &dockerclient.APIVersionError{ServerMin: "1.50", ClientMax: "1.44"}
	fmt.Println(err)
	// Output: docker: the docker daemon requires API 1.50 or newer but this client supports at most 1.44. Upgrade mongotest, or set DOCKER_API_VERSION to a version the daemon accepts if you know it works
}

func ExampleStatusError_Hint() {
	// Hint turns a bare status code into the reason it usually has, which is
	// what a caller wants to print when the daemon's own message is terse.
	for _, err := range []*dockerclient.StatusError{
		{StatusCode: 404, Message: "No such container: c1"},
		{StatusCode: 409, Message: "container c1 is running"},
		{StatusCode: 401, Message: "unauthorized"},
	} {
		fmt.Printf("%d: %s\n", err.StatusCode, err.Hint())
	}
	// Output:
	// 404: Check the id, name or image tag; the object may have been removed already, or the image was never pulled
	// 409: Another operation on this object is in progress or its name is taken; retry shortly, pick another name, or remove with Force
	// 401: Credentials were refused; log in with `docker login`, or use an image from a public registry
}

func ExamplePullError() {
	// A pull answers 200 and then reports the failure inside the progress
	// stream, so this error only exists because the stream was read to the end.
	err := &dockerclient.PullError{
		Ref:     "mongo:nosuchtag",
		Message: "manifest for mongo:nosuchtag not found",
	}
	fmt.Println(err)
	fmt.Println(errors.Is(err, dockerclient.ErrPull))
	// Output:
	// docker: pulling mongo:nosuchtag failed: manifest for mongo:nosuchtag not found. Check the image name and tag exist on the registry and that this host can reach it
	// true
}

func ExampleResponseError() {
	// The daemon answered, but the answer could not be used. Unwrap reaches
	// the decode failure underneath.
	err := dockerclient.DecodeError(http.MethodGet, "/containers/c1/json", io.ErrUnexpectedEOF)
	fmt.Println(err)
	fmt.Println(errors.Is(err, dockerclient.ErrDaemonResponse))
	fmt.Println(errors.Is(err, io.ErrUnexpectedEOF))
	// Output:
	// docker: GET /containers/c1/json: could not decode the daemon's response: unexpected EOF. This usually means the daemon speaks a different API version than negotiated; check `docker version` and any DOCKER_API_VERSION pin
	// true
	// true
}

func ExampleStreamError() {
	// The daemon wrote an error frame into the exec stream, which means the
	// command never ran to completion.
	err := &dockerclient.StreamError{Problem: "the container stopped during the exec"}
	fmt.Println(err)
	fmt.Println(errors.Is(err, dockerclient.ErrStream))
	// Output:
	// docker: the container stopped during the exec
	// true
}

func ExampleConnectionError() {
	// A connection failure names the host it tried and what to do about it,
	// because "connection refused" on its own tells a caller nothing.
	err := dockerclient.ConnectionFailed("unix:///var/run/docker.sock", "connection refused",
		"start docker, or set DOCKER_HOST to a daemon that is running", nil)
	fmt.Println(err)
	fmt.Println(errors.Is(err, dockerclient.ErrConnectionFailed))
	// Output:
	// docker: cannot connect to the docker daemon at unix:///var/run/docker.sock: connection refused. start docker, or set DOCKER_HOST to a daemon that is running
	// true
}

func ExampleInvalidArgumentError() {
	// Every validation failure carries the argument, the value, the problem
	// and the fix, so the four parts can be reported separately if wanted.
	err := dockerclient.InvalidArgument("host port", "70000", "ports run to 65535",
		"pass an empty host port and let the daemon choose one")
	var invalid *dockerclient.InvalidArgumentError
	if errors.As(err, &invalid) {
		fmt.Println("argument:", invalid.Argument)
		fmt.Println("value:", invalid.Value)
		fmt.Println("problem:", invalid.Problem)
		fmt.Println("fix:", invalid.Fix)
	}
	// Output:
	// argument: host port
	// value: 70000
	// problem: ports run to 65535
	// fix: pass an empty host port and let the daemon choose one
}
