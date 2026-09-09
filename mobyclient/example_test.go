package mobyclient_test

import (
	"archive/tar"
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"

	"github.com/moby/moby/client"

	"github.com/tophergopher/mongotest/dockerclient"
	"github.com/tophergopher/mongotest/dockermock"
	"github.com/tophergopher/mongotest/mobyclient"
)

// The examples in this file talk to an in-process stand-in for the Docker
// Engine API (see exampleDaemon at the bottom of the file), so they run
// anywhere and produce the same output with no Docker daemon installed.
// Real code gets its client from mobyclient.FromEnv instead.

func ExampleFromEnv() {
	// FromEnv locates the daemon like the docker CLI: DOCKER_HOST, then
	// DOCKER_API_VERSION, DOCKER_CERT_PATH and DOCKER_TLS_VERIFY for TLS.
	os.Setenv("DOCKER_HOST", stubHost())
	defer os.Unsetenv("DOCKER_HOST")

	c, err := mobyclient.FromEnv()
	if err != nil {
		fmt.Println("cannot reach a daemon:", err)
		return
	}
	fmt.Println("using the daemon from DOCKER_HOST:", c.Host() == os.Getenv("DOCKER_HOST"))
	// Output: using the daemon from DOCKER_HOST: true
}

func ExampleNew() {
	c, err := mobyclient.New(mobyclient.WithHost(stubHost()))
	if err != nil {
		panic(err)
	}
	fmt.Println(c.Host() == stubHost())
	// Output: true
}

func ExampleWithHost() {
	// A unix socket, a TCP endpoint, or an http/https URL.
	c, err := mobyclient.New(mobyclient.WithHost("unix:///var/run/docker.sock"))
	if err != nil {
		panic(err)
	}
	fmt.Println(c.Host())
	// Output: unix:///var/run/docker.sock
}

func ExampleWithMobyClient() {
	// Wrap a *client.Client the caller already built, instead of letting
	// mobyclient construct one.
	moby, err := client.New(client.WithHost(stubHost()))
	if err != nil {
		panic(err)
	}
	c, err := mobyclient.New(mobyclient.WithMobyClient(moby))
	if err != nil {
		panic(err)
	}
	fmt.Println(c.Host() == stubHost())
	// Output: true
}

// debugCounter is a minimal dockerclient.Logger that counts Debug calls, so
// ExampleWithLogger does not depend on exactly how many requests one call
// makes under the hood (version negotiation adds one the first time).
type debugCounter struct{ n int }

func (c *debugCounter) Debug(string, ...any) { c.n++ }
func (*debugCounter) Info(string, ...any)    {}
func (*debugCounter) Warn(string, ...any)    {}
func (*debugCounter) Error(string, ...any)   {}

func ExampleWithLogger() {
	// One Debug record per request, via the moby client's response hooks.
	// Pass slog.Default() in real code.
	counter := &debugCounter{}
	c, err := mobyclient.New(mobyclient.WithHost(stubHost()), mobyclient.WithLogger(counter))
	if err != nil {
		panic(err)
	}
	if _, err := c.ImageInspect(context.Background(), "mongo:8"); err != nil {
		panic(err)
	}
	fmt.Println(counter.n > 0)
	// Output: true
}

func ExampleClient_Host() {
	c := exampleClient()
	fmt.Println(strings.HasPrefix(c.Host(), "tcp://"))
	// Output: true
}

func ExampleClient_ImagePull() {
	c := exampleClient()
	if err := c.ImagePull(context.Background(), "mongo:8"); err != nil {
		if errors.Is(err, dockerclient.ErrUnauthorized) {
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
	if dockerclient.IsNotFound(err) {
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
	cfg := dockerclient.ContainerConfig{
		Image:        "mongo:8",
		Cmd:          []string{"--replSet", "rs0"}, // the image entrypoint prepends mongod
		Labels:       map[string]string{"mongotest": "regression"},
		ExposedPorts: map[string]struct{}{"27017/tcp": {}},
		HostConfig: &dockerclient.HostConfig{PortBindings: map[string][]dockerclient.PortBinding{
			// An empty HostPort lets the daemon pick a free one; read it
			// back with ContainerInspect.
			"27017/tcp": {{HostIP: "127.0.0.1", HostPort: ""}},
		}},
	}
	id, warnings, err := c.ContainerCreate(ctx, "mongotest-example", cfg)
	if dockerclient.IsNotFound(err) {
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
	if err := c.ContainerStart(context.Background(), dockermock.DefaultContainerID); err != nil {
		panic(err)
	}
	fmt.Println("started")
	// Output: started
}

func ExampleClient_ContainerInspect() {
	c := exampleClient()
	info, err := c.ContainerInspect(context.Background(), dockermock.DefaultContainerID)
	if err != nil {
		panic(err)
	}
	fmt.Println(info.Name, info.State.Status, info.State.Running)
	// Output: /mongotest-example running true
}

func ExampleClient_ContainerTop() {
	c := exampleClient()
	top, err := c.ContainerTop(context.Background(), dockermock.DefaultContainerID)
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
	err := c.ContainerRemove(context.Background(), dockermock.DefaultContainerID, dockerclient.RemoveOptions{
		Force:         true, // kill it first if it is still running
		RemoveVolumes: true,
	})
	// Cleanup code can treat "already gone" as success.
	if err != nil && !dockerclient.IsNotFound(err) {
		panic(err)
	}
	fmt.Println("removed")
	// Output: removed
}

func ExampleClient_CopyToContainer() {
	c := exampleClient()
	// This works on a created but not yet started container, so files are in
	// place before the process runs. The archive is streamed, not buffered.
	err := c.CopyToContainer(context.Background(), dockermock.DefaultContainerID, "/etc", []dockerclient.File{
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

	if err := c.CopyArchiveToContainer(context.Background(), dockermock.DefaultContainerID, "/tmp", &buf); err != nil {
		panic(err)
	}
	fmt.Println("seed.js is in /tmp")
	// Output: seed.js is in /tmp
}

func ExampleClient_ExecCreate() {
	c := exampleClient()
	ctx := context.Background()
	execID, err := c.ExecCreate(ctx, dockermock.DefaultContainerID, dockerclient.ExecConfig{
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

func ExampleClient_ExecStartTo() {
	c := exampleClient()
	ctx := context.Background()
	execID, err := c.ExecCreate(ctx, dockermock.DefaultContainerID, dockerclient.ExecConfig{
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
	ins, err := c.ExecInspect(context.Background(), dockermock.DefaultExecID)
	if err != nil {
		panic(err)
	}
	fmt.Println(ins.Running, ins.ExitCode)
	// Output: false 0
}

func ExampleClient_Exec() {
	c := exampleClient()
	res, err := c.Exec(context.Background(), dockermock.DefaultContainerID,
		"mongosh", "--quiet", "--eval", "db.runCommand({ping:1}).ok")
	if err != nil {
		panic(err) // a transport or daemon failure
	}
	// A non-zero exit code is reported in the result, not as an error.
	fmt.Printf("exit %d, stdout %q\n", res.ExitCode, res.Stdout)
	// Output: exit 0, stdout "1\n"
}

// --- the stub daemon backing the examples above ---------------------------

// exampleDaemon is one fake Docker daemon shared by every example in this
// file, so they run anywhere with no Docker installed and always produce the
// same output. ServeDefaults answers every endpoint with the fixed values
// dockermock documents; servePing adds the /_ping route the moby client
// negotiates against. Real code has no stub: it calls mobyclient.FromEnv.
var exampleDaemon = sync.OnceValue(func() *dockermock.Daemon {
	d := dockermock.NewDaemon(dockermock.OverTCP())
	d.ServeDefaults()
	servePing(d)
	return d
})

// stubHost returns the fake daemon's address in DOCKER_HOST form.
func stubHost() string { return exampleDaemon().Host() }

// exampleClient returns a client wired to the fake daemon.
func exampleClient() *mobyclient.Client {
	c, err := mobyclient.New(mobyclient.WithHost(stubHost()))
	if err != nil {
		panic(err)
	}
	return c
}
