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
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port
}

// liveClient connects to the real daemon or fails the test with guidance.
func liveClient(t *testing.T) (*Client, context.Context) {
	t.Helper()
	c, err := FromEnv()
	if err != nil {
		t.Fatalf("dockerapi integration: cannot configure a Docker client: %v; set DOCKER_HOST to point at a running Docker daemon", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	t.Cleanup(cancel)
	if err := c.Negotiate(ctx); err != nil {
		t.Fatalf("dockerapi integration: no Docker daemon reachable at %s: %v; set DOCKER_HOST to point at a running Docker daemon", c.Host(), err)
	}
	return c, ctx
}

func TestIntegrationNegotiate(t *testing.T) {
	c, _ := liveClient(t)
	v := c.APIVersion()
	if compareVersions(v, MinSupportedAPIVersion) < 0 || compareVersions(v, PreferredAPIVersion) > 0 {
		t.Fatalf("negotiated %q outside [%s, %s]", v, MinSupportedAPIVersion, PreferredAPIVersion)
	}
	t.Logf("daemon %s negotiated API %s", c.Host(), v)
}

func TestIntegrationImagePullAndInspect(t *testing.T) {
	c, ctx := liveClient(t)
	img := integrationImage()
	if err := c.ImagePull(ctx, img); err != nil {
		t.Fatalf("pull %s: %v", img, err)
	}
	info, err := c.ImageInspect(ctx, img)
	if err != nil {
		t.Fatalf("inspect %s after pull: %v", img, err)
	}
	if info.ID == "" || info.OS != "linux" {
		t.Fatalf("inspect returned %+v", info)
	}
	if _, err := c.ImageInspect(ctx, "mongotest-no-such-image:zzz"); !IsNotFound(err) {
		t.Fatalf("missing image: want ErrNotFound, got %v", err)
	}
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
		if err := c.ImagePull(ctx, img); err != nil {
			t.Fatalf("pull %s: %v", img, err)
		}
		id, _, err = c.ContainerCreate(ctx, name, cfg)
	}
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	t.Cleanup(func() {
		_ = c.ContainerRemove(context.Background(), id, RemoveOptions{Force: true, RemoveVolumes: true})
	})

	// Copy before start works on a created container.
	if err := c.CopyToContainer(ctx, id, "/tmp", []File{{Name: "probe/hello.txt", Content: []byte("hello from dockerapi\n")}}); err != nil {
		t.Fatalf("copy before start: %v", err)
	}

	if err := c.ContainerStart(ctx, id); err != nil {
		t.Fatalf("start: %v", err)
	}
	if err := c.ContainerStart(ctx, id); err != nil {
		t.Fatalf("second start must be a no-op: %v", err)
	}

	info, err := c.ContainerInspect(ctx, id)
	if err != nil {
		t.Fatalf("inspect: %v", err)
	}
	if !info.State.Running || info.HostPort("27017/tcp") != strconv.Itoa(port) || info.Name != "/"+name {
		t.Fatalf("inspect: %+v", info)
	}

	// Wait for the published port to accept connections, then check the
	// process table names mongod.
	deadline := time.Now().Add(60 * time.Second)
	for {
		conn, err := net.DialTimeout("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(port)), 500*time.Millisecond)
		if err == nil {
			conn.Close()
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("port %d never accepted connections", port)
		}
		time.Sleep(200 * time.Millisecond)
	}
	top, err := c.ContainerTop(ctx, id)
	if err != nil {
		t.Fatalf("top: %v", err)
	}
	found := false
	for _, p := range top.Processes {
		if strings.Contains(strings.Join(p, " "), "mongod") {
			found = true
		}
	}
	if !found {
		t.Fatalf("no mongod process in top: %+v", top)
	}

	res, err := c.Exec(ctx, id, "cat", "/tmp/probe/hello.txt")
	if err != nil {
		t.Fatalf("exec cat: %v", err)
	}
	if res.ExitCode != 0 || res.Stdout != "hello from dockerapi\n" || res.Stderr != "" {
		t.Fatalf("exec cat: %+v", res)
	}

	res, err = c.Exec(ctx, id, "sh", "-c", "echo to-stderr >&2; exit 7")
	if err != nil {
		t.Fatalf("exec failing command: %v", err)
	}
	if res.ExitCode != 7 || res.Stderr != "to-stderr\n" || res.Stdout != "" {
		t.Fatalf("exec failing command: %+v", res)
	}

	if err := c.ContainerRemove(ctx, id, RemoveOptions{Force: true, RemoveVolumes: true}); err != nil {
		t.Fatalf("remove: %v", err)
	}
	if err := c.ContainerRemove(ctx, id, RemoveOptions{Force: true}); !IsNotFound(err) {
		t.Fatalf("second remove: want ErrNotFound, got %v", err)
	}
	if _, err := c.ContainerInspect(ctx, id); !IsNotFound(err) {
		t.Fatalf("inspect after remove: want ErrNotFound, got %v", err)
	}
	if err := c.ContainerStart(ctx, id); !IsNotFound(err) {
		t.Fatalf("start after remove: want ErrNotFound, got %v", err)
	}
}

func TestIntegrationUnreachableDaemonMessage(t *testing.T) {
	// Not a live test, but it pins the guidance callers see when the daemon
	// is down, which the other tests rely on.
	c, err := New(WithHost("unix:///nonexistent/docker.sock"))
	if err != nil {
		t.Fatal(err)
	}
	err = c.Negotiate(context.Background())
	if err == nil || !strings.Contains(err.Error(), "/nonexistent/docker.sock") {
		t.Fatalf("err = %v", err)
	}
	var se *StatusError
	if errors.As(err, &se) {
		t.Fatal("a dial failure must not be a StatusError")
	}
}

// Many containers at once: parallel subtests sharing one client, each
// letting the daemon pick the host port (HostPort "") so there is no port
// race between them, plus a plain goroutine fan-out on the same client.
func TestIntegrationParallelContainers(t *testing.T) {
	c, ctx := liveClient(t)
	img := integrationImage()
	if err := c.ImagePull(ctx, img); err != nil {
		t.Fatalf("pull %s: %v", img, err)
	}

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
		if err != nil {
			t.Fatalf("create: %v", err)
		}
		defer func() {
			_ = c.ContainerRemove(context.Background(), id, RemoveOptions{Force: true, RemoveVolumes: true})
		}()
		if err := c.ContainerStart(ctx, id); err != nil {
			t.Fatalf("start: %v", err)
		}
		info, err := c.ContainerInspect(ctx, id)
		if err != nil {
			t.Fatalf("inspect: %v", err)
		}
		hostPort := info.HostPort("27017/tcp")
		if hostPort == "" {
			t.Fatalf("daemon did not assign a host port: %+v", info.NetworkSettings.Ports)
		}
		deadline := time.Now().Add(90 * time.Second)
		for {
			conn, err := net.DialTimeout("tcp", net.JoinHostPort("127.0.0.1", hostPort), 500*time.Millisecond)
			if err == nil {
				conn.Close()
				break
			}
			if time.Now().After(deadline) {
				t.Fatalf("port %s never accepted connections", hostPort)
			}
			time.Sleep(200 * time.Millisecond)
		}
		res, err := c.Exec(ctx, id, "sh", "-c", "echo "+tag)
		if err != nil || res.ExitCode != 0 || strings.TrimSpace(res.Stdout) != tag {
			t.Fatalf("exec: %+v %v", res, err)
		}
		if err := c.ContainerRemove(ctx, id, RemoveOptions{Force: true, RemoveVolumes: true}); err != nil {
			t.Fatalf("remove: %v", err)
		}
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
			t.Error(err)
		}
	})

	left, err := exec.Command("docker", "ps", "-aq", "--filter", "label=mongotest.parallel").Output()
	if err == nil && strings.TrimSpace(string(left)) != "" {
		t.Fatalf("containers left behind: %s", left)
	}
}

// fanoutT lets run() be driven from a goroutine (testing.T must not be used
// from goroutines that outlive the test's Fatal).
type fanoutT struct {
	testing.TB
	err error
}

var errFanoutFatal = errors.New("fanout fatal")

func (f *fanoutT) Helper() {}
func (f *fanoutT) Fatalf(format string, args ...any) {
	f.err = fmt.Errorf(format, args...)
	panic(errFanoutFatal)
}
func (f *fanoutT) Errorf(format string, args ...any) { f.err = fmt.Errorf(format, args...) }
func (f *fanoutT) Logf(string, ...any)               {}
