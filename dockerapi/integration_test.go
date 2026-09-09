package dockerapi

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// These tests talk to a real Docker daemon located through the normal
// environment (DOCKER_HOST or the default socket). They fail, rather than
// skip, when no daemon is reachable: mongotest is a Docker-based test helper
// and a silently skipped suite would hide a broken client.

func integrationImage() string {
	if img := os.Getenv("MONGOTEST_IMAGE"); img != "" {
		return img
	}
	return "mongo:8"
}

func freePort(t *testing.T) int {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err, "reserving a free loopback port")
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port
}

// liveClient connects to the real daemon or fails the test with guidance.
func liveClient(t testing.TB) (*Client, context.Context) {
	t.Helper()
	c, err := FromEnv()
	require.NoError(t, err, "dockerapi integration: cannot configure a Docker client; set DOCKER_HOST to point at a running Docker daemon")
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	t.Cleanup(cancel)
	require.NoError(t, c.Negotiate(ctx), "dockerapi integration: no Docker daemon reachable at %s; set DOCKER_HOST to point at a running Docker daemon", c.Host())
	return c, ctx
}

// waitForPort blocks until something accepts TCP connections on 127.0.0.1:port.
func waitForPort(t testing.TB, port string, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for {
		conn, err := net.DialTimeout("tcp", net.JoinHostPort("127.0.0.1", port), 500*time.Millisecond)
		if err == nil {
			conn.Close()
			return
		}
		require.False(t, time.Now().After(deadline), "port %s never accepted connections within %s; mongod did not start", port, timeout)
		time.Sleep(200 * time.Millisecond)
	}
}

func TestIntegrationNegotiate(t *testing.T) {
	c, _ := liveClient(t)
	v := c.APIVersion()
	assert.GreaterOrEqual(t, compareVersions(v, MinSupportedAPIVersion), 0, "negotiated %q must not be below the client minimum %s", v, MinSupportedAPIVersion)
	assert.LessOrEqual(t, compareVersions(v, PreferredAPIVersion), 0, "negotiated %q must not exceed the client preference %s", v, PreferredAPIVersion)
	t.Logf("daemon %s negotiated API %s", c.Host(), v)
}

func TestIntegrationImagePullAndInspect(t *testing.T) {
	c, ctx := liveClient(t)
	img := integrationImage()
	require.NoError(t, c.ImagePull(ctx, img), "pulling %s from the registry", img)
	info, err := c.ImageInspect(ctx, img)
	require.NoError(t, err, "inspecting %s right after pulling it", img)
	assert.NotEmpty(t, info.ID, "a pulled image has an id")
	assert.Equal(t, "linux", info.OS, "the mongo image is a linux image")
	_, err = c.ImageInspect(ctx, "mongotest-no-such-image:zzz")
	assert.True(t, IsNotFound(err), "an image that was never pulled must be ErrNotFound, got %v", err)
}

func TestIntegrationContainerLifecycle(t *testing.T) {
	c, ctx := liveClient(t)
	img := integrationImage()
	port := freePort(t)
	name := "dockerapi-it-" + strconv.Itoa(port)

	cfg := ContainerConfig{
		Image:        img,
		Labels:       map[string]string{"mongotest": "regression"},
		ExposedPorts: map[string]struct{}{"27017/tcp": {}},
		HostConfig: &HostConfig{PortBindings: map[string][]PortBinding{
			"27017/tcp": {{HostIP: "127.0.0.1", HostPort: strconv.Itoa(port)}},
		}},
	}
	id, _, err := c.ContainerCreate(ctx, name, cfg)
	if IsNotFound(err) {
		require.NoError(t, c.ImagePull(ctx, img), "pulling %s after the daemon reported it missing", img)
		id, _, err = c.ContainerCreate(ctx, name, cfg)
	}
	require.NoError(t, err, "creating the container")
	t.Cleanup(func() {
		_ = c.ContainerRemove(context.Background(), id, RemoveOptions{Force: true, RemoveVolumes: true})
	})

	require.NoError(t, c.CopyToContainer(ctx, id, "/tmp", []File{{Name: "probe/hello.txt", Content: []byte("hello from dockerapi\n")}}),
		"copying into a created but not yet started container must work; the TLS flow depends on it")

	require.NoError(t, c.ContainerStart(ctx, id), "starting the container")
	require.NoError(t, c.ContainerStart(ctx, id), "a second start must be a no-op (304)")

	info, err := c.ContainerInspect(ctx, id)
	require.NoError(t, err, "inspecting the running container")
	assert.True(t, info.State.Running, "the container must be running after start")
	assert.Equal(t, strconv.Itoa(port), info.HostPort("27017/tcp"), "inspect must report the pinned host port")
	assert.Equal(t, "/"+name, info.Name, "the daemon reports the name with a leading slash")

	waitForPort(t, strconv.Itoa(port), 60*time.Second)
	top, err := c.ContainerTop(ctx, id)
	require.NoError(t, err, "listing processes")
	found := false
	for _, p := range top.Processes {
		if strings.Contains(strings.Join(p, " "), "mongod") {
			found = true
		}
	}
	assert.True(t, found, "a mongod process must appear in top, got %+v", top.Processes)

	res, err := c.Exec(ctx, id, "cat", "/tmp/probe/hello.txt")
	require.NoError(t, err, "exec cat of the copied file")
	assert.Equal(t, ExecResult{Stdout: "hello from dockerapi\n", ExitCode: 0}, res, "the copied file must be readable inside the container with a clean exit")

	res, err = c.Exec(ctx, id, "sh", "-c", "echo to-stderr >&2; exit 7")
	require.NoError(t, err, "exec of a failing command is not itself an error")
	assert.Equal(t, ExecResult{Stderr: "to-stderr\n", ExitCode: 7}, res, "stderr and the exit code must be reported separately from stdout")

	require.NoError(t, c.ContainerRemove(ctx, id, RemoveOptions{Force: true, RemoveVolumes: true}), "removing the running container with force")
	assert.True(t, IsNotFound(c.ContainerRemove(ctx, id, RemoveOptions{Force: true})), "a second remove must be ErrNotFound")
	_, err = c.ContainerInspect(ctx, id)
	assert.True(t, IsNotFound(err), "inspect after remove must be ErrNotFound, got %v", err)
	assert.True(t, IsNotFound(c.ContainerStart(ctx, id)), "start after remove must be ErrNotFound")
}

func TestIntegrationUnreachableDaemonMessage(t *testing.T) {
	// Not a live test, but it pins the guidance callers see when the daemon
	// is down, which the other tests rely on.
	c, err := New(WithHost("unix:///nonexistent/docker.sock"))
	require.NoError(t, err, "construction does not dial")
	err = c.Negotiate(context.Background())
	require.ErrorIs(t, err, ErrConnectionFailed, "an unreachable socket is a connection failure")
	assert.Contains(t, err.Error(), "/nonexistent/docker.sock", "the error names the socket")
	var se *StatusError
	assert.False(t, errors.As(err, &se), "a dial failure must not be reported as a daemon status error")
}

// Many containers at once: parallel subtests sharing one client, each
// letting the daemon pick the host port (HostPort "") so there is no port
// race between them, plus a plain goroutine fan-out on the same client.
func TestIntegrationParallelContainers(t *testing.T) {
	c, ctx := liveClient(t)
	img := integrationImage()
	require.NoError(t, c.ImagePull(ctx, img), "pulling %s before the parallel runs", img)

	run := func(t testing.TB, tag string) {
		t.Helper()
		cfg := ContainerConfig{
			Image:        img,
			Labels:       map[string]string{"mongotest": "regression", "mongotest.parallel": tag},
			ExposedPorts: map[string]struct{}{"27017/tcp": {}},
			HostConfig: &HostConfig{PortBindings: map[string][]PortBinding{
				"27017/tcp": {{HostIP: "127.0.0.1", HostPort: ""}},
			}},
		}
		id, _, err := c.ContainerCreate(ctx, "", cfg)
		require.NoError(t, err, "%s: create", tag)
		defer func() {
			_ = c.ContainerRemove(context.Background(), id, RemoveOptions{Force: true, RemoveVolumes: true})
		}()
		require.NoError(t, c.ContainerStart(ctx, id), "%s: start", tag)
		info, err := c.ContainerInspect(ctx, id)
		require.NoError(t, err, "%s: inspect", tag)
		hostPort := info.HostPort("27017/tcp")
		require.NotEmpty(t, hostPort, "%s: the daemon must assign a host port when HostPort is empty, got %+v", tag, info.NetworkSettings.Ports)
		waitForPort(t, hostPort, 90*time.Second)
		res, err := c.Exec(ctx, id, "sh", "-c", "echo "+tag)
		require.NoError(t, err, "%s: exec", tag)
		assert.Equal(t, 0, res.ExitCode, "%s: echo exits 0", tag)
		assert.Equal(t, tag, strings.TrimSpace(res.Stdout), "%s: exec output must come from this container, not another parallel one", tag)
		require.NoError(t, c.ContainerRemove(ctx, id, RemoveOptions{Force: true, RemoveVolumes: true}), "%s: remove", tag)
	}

	t.Run("subtests", func(t *testing.T) {
		for i := 0; i < 6; i++ {
			tag := "sub-" + strconv.Itoa(i)
			t.Run(tag, func(t *testing.T) {
				t.Parallel()
				run(t, tag)
			})
		}
	})

	t.Run("goroutines", func(t *testing.T) {
		var wg sync.WaitGroup
		errs := make(chan error, 6)
		for i := 0; i < 6; i++ {
			wg.Add(1)
			go func(i int) {
				defer wg.Done()
				ft := &fanoutT{}
				func() {
					defer func() {
						if r := recover(); r != nil && r != errFanoutFatal {
							panic(r)
						}
					}()
					run(ft, "go-"+strconv.Itoa(i))
				}()
				if ft.err != nil {
					errs <- ft.err
				}
			}(i)
		}
		wg.Wait()
		close(errs)
		for err := range errs {
			assert.NoError(t, err, "every goroutine-driven lifecycle must complete")
		}
	})

	left, err := exec.Command("docker", "ps", "-aq", "--filter", "label=mongotest.parallel").Output()
	if err == nil {
		assert.Empty(t, strings.TrimSpace(string(left)), "no parallel-test containers may be left behind")
	}
}

// fanoutT lets run() be driven from a goroutine: testing.T must not be used
// from goroutines that outlive the test's Fatal, so failures are captured
// and reported from the parent instead.
type fanoutT struct {
	testing.TB
	err error
}

var errFanoutFatal = errors.New("fanout fatal")

func (f *fanoutT) Helper()        {}
func (f *fanoutT) Cleanup(func()) {}
func (f *fanoutT) Errorf(format string, args ...any) {
	if f.err == nil {
		f.err = fmt.Errorf(format, args...)
	}
}
func (f *fanoutT) FailNow()            { panic(errFanoutFatal) }
func (f *fanoutT) Logf(string, ...any) {}
