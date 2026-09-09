package dockerclienttest

import (
	"bytes"
	"context"
	"errors"
	"testing"

	"github.com/tophergopher/mongotest/dockerclient"
)

// Factory returns a client whose daemon starts empty and keeps state for the
// duration of one test.
type Factory func(t *testing.T) dockerclient.Client

// TestImage is the image the conformance suite pulls and runs. Nothing is
// downloaded: the daemon behind the client is a double.
const TestImage = "mongo:8"

// Conformance runs every behaviour assertion against one implementation.
func Conformance(t *testing.T, newClient Factory) {
	t.Run("rejects bad input before reaching the daemon", func(t *testing.T) {
		conformanceValidation(t, newClient(t))
	})
	t.Run("reports a missing object as ErrNotFound", func(t *testing.T) {
		conformanceNotFound(t, newClient(t))
	})
	t.Run("runs a container lifecycle", func(t *testing.T) {
		conformanceLifecycle(t, newClient(t))
	})
	t.Run("copies files into a container", func(t *testing.T) {
		conformanceCopy(t, newClient(t))
	})
	t.Run("runs a command and reports its output", func(t *testing.T) {
		conformanceExec(t, newClient(t))
	})
}

func conformanceValidation(t *testing.T, c dockerclient.Client) {
	ctx := context.Background()
	checks := map[string]func() error{
		"an uppercase image reference": func() error { return c.ImagePull(ctx, "Mongo:8") },
		"an empty container id":        func() error { return c.ContainerStart(ctx, "") },
		"a container id with a slash": func() error {
			return c.ContainerRemove(ctx, "a/b", dockerclient.RemoveOptions{})
		},
		"a create with no image": func() error {
			_, _, err := c.ContainerCreate(ctx, "", dockerclient.ContainerConfig{})
			return err
		},
		"an empty exec id": func() error {
			return c.ExecStartTo(ctx, "", nil, nil)
		},
		"an exec id with a slash": func() error {
			_, err := c.ExecInspect(ctx, "a/b")
			return err
		},
		"a port binding with a bad host ip": func() error {
			_, _, err := c.ContainerCreate(ctx, "", dockerclient.ContainerConfig{
				Image: TestImage,
				HostConfig: &dockerclient.HostConfig{PortBindings: map[string][]dockerclient.PortBinding{
					"27017/tcp": {{HostIP: "not-an-ip"}},
				}},
			})
			return err
		},
		"a copy with no files": func() error {
			return c.CopyToContainer(ctx, "abc", "/tmp", nil)
		},
		"an exec with no command": func() error {
			_, err := c.Exec(ctx, "abc")
			return err
		},
	}
	for name, check := range checks {
		err := check()
		if !errors.Is(err, dockerclient.ErrInvalidArgument) {
			t.Errorf("%s must be rejected as an invalid argument, got %v", name, err)
		}
	}
}

func conformanceNotFound(t *testing.T, c dockerclient.Client) {
	ctx := context.Background()
	checks := map[string]func() error{
		"inspecting an unknown container": func() error {
			_, err := c.ContainerInspect(ctx, "missing")
			return err
		},
		"starting an unknown container": func() error { return c.ContainerStart(ctx, "missing") },
		"removing an unknown container": func() error {
			return c.ContainerRemove(ctx, "missing", dockerclient.RemoveOptions{Force: true})
		},
		"inspecting an image that was never pulled": func() error {
			_, err := c.ImageInspect(ctx, "never-pulled:1")
			return err
		},
		"creating from an image that was never pulled": func() error {
			_, _, err := c.ContainerCreate(ctx, "", dockerclient.ContainerConfig{Image: "never-pulled:1"})
			return err
		},
	}
	for name, check := range checks {
		err := check()
		if !dockerclient.IsNotFound(err) {
			t.Errorf("%s must report ErrNotFound, got %v", name, err)
		}
	}
}

// createRunning pulls the test image and starts a container from it.
func createRunning(t *testing.T, c dockerclient.Client) string {
	t.Helper()
	ctx := context.Background()
	if err := c.ImagePull(ctx, TestImage); err != nil {
		t.Fatalf("pulling %s: %v", TestImage, err)
	}
	id, _, err := c.ContainerCreate(ctx, "", dockerclient.ContainerConfig{
		Image:        TestImage,
		Labels:       map[string]string{"mongotest": "conformance"},
		ExposedPorts: map[string]struct{}{"27017/tcp": {}},
		HostConfig: &dockerclient.HostConfig{PortBindings: map[string][]dockerclient.PortBinding{
			"27017/tcp": {{HostIP: "127.0.0.1", HostPort: ""}},
		}},
	})
	if err != nil {
		t.Fatalf("creating a container: %v", err)
	}
	if err := c.ContainerStart(ctx, id); err != nil {
		t.Fatalf("starting %s: %v", id, err)
	}
	return id
}

func conformanceLifecycle(t *testing.T, c dockerclient.Client) {
	ctx := context.Background()
	id := createRunning(t, c)

	if err := c.ContainerStart(ctx, id); err != nil {
		t.Errorf("starting an already running container must succeed, got %v", err)
	}

	info, err := c.ContainerInspect(ctx, id)
	if err != nil {
		t.Fatalf("inspecting %s: %v", id, err)
	}
	if !info.State.Running {
		t.Errorf("a started container must report Running, got state %+v", info.State)
	}
	if info.HostPort("27017/tcp") == "" {
		t.Errorf("an empty HostPort must be filled in by the daemon, got ports %+v", info.NetworkSettings.Ports)
	}
	if info.HostPort("80/tcp") != "" {
		t.Errorf("HostPort must be empty for a port that was never published, got %q", info.HostPort("80/tcp"))
	}

	top, err := c.ContainerTop(ctx, id)
	if err != nil {
		t.Fatalf("listing processes in %s: %v", id, err)
	}
	if len(top.Processes) == 0 {
		t.Error("a running container must report at least one process")
	}

	if err := c.ContainerRemove(ctx, id, dockerclient.RemoveOptions{Force: true, RemoveVolumes: true}); err != nil {
		t.Fatalf("removing %s: %v", id, err)
	}
	if _, err := c.ContainerInspect(ctx, id); !dockerclient.IsNotFound(err) {
		t.Errorf("inspecting a removed container must report ErrNotFound, got %v", err)
	}
	if err := c.ContainerRemove(ctx, id, dockerclient.RemoveOptions{Force: true}); !dockerclient.IsNotFound(err) {
		t.Errorf("removing a container twice must report ErrNotFound, got %v", err)
	}
}

func conformanceCopy(t *testing.T, c dockerclient.Client) {
	ctx := context.Background()
	id := createRunning(t, c)
	files := []dockerclient.File{
		{Name: "mongo-tls/server.pem", Mode: 0o644, Content: []byte("certificate and key")},
		{Name: "mongo-tls/ca.pem", Mode: 0o644, Content: []byte("ca")},
	}
	// One condition per assertion, each checking the sentinel rather than
	// merely that something failed. An earlier version passed id and destDir
	// the wrong way round and asserted only err != nil, so it passed for the
	// wrong reason and hid two missing checks in the Fake.
	if err := c.CopyToContainer(ctx, id, "/etc", nil); !errors.Is(err, dockerclient.ErrNoFiles) {
		t.Errorf("a copy with no files must be ErrNoFiles, got %v", err)
	}
	if err := c.CopyToContainer(ctx, id, "", files); !errors.Is(err, dockerclient.ErrInvalidArgument) {
		t.Errorf("a copy with no destination must be ErrInvalidArgument, got %v", err)
	}
	if err := c.CopyToContainer(ctx, id, "relative", files); !errors.Is(err, dockerclient.ErrInvalidArgument) {
		t.Errorf("a relative destination must be rejected, got %v", err)
	}
	if err := c.CopyToContainer(ctx, id, "/etc/../root", files); !errors.Is(err, dockerclient.ErrInvalidArgument) {
		t.Errorf("a destination that walks outside itself must be rejected, got %v", err)
	}
	if err := c.CopyArchiveToContainer(ctx, id, "relative", bytes.NewReader(nil)); !errors.Is(err, dockerclient.ErrInvalidArgument) {
		t.Errorf("CopyArchiveToContainer must apply the same destination rule, got %v", err)
	}
	if err := c.CopyToContainer(ctx, id, "/etc", files); err != nil {
		t.Fatalf("copying files into %s: %v", id, err)
	}
	if err := c.CopyArchiveToContainer(ctx, id, "/tmp", bytes.NewReader(nil)); err != nil {
		t.Errorf("an empty archive is not an error, got %v", err)
	}
}

func conformanceExec(t *testing.T, c dockerclient.Client) {
	ctx := context.Background()
	id := createRunning(t, c)

	res, err := c.Exec(ctx, id, "echo", "hello")
	if err != nil {
		t.Fatalf("running a command in %s: %v", id, err)
	}
	if res.ExitCode != 0 {
		t.Errorf("a successful command must report exit code 0, got %+v", res)
	}

	execID, err := c.ExecCreate(ctx, id, dockerclient.ExecConfig{Cmd: []string{"echo", "streamed"}})
	if err != nil {
		t.Fatalf("creating an exec in %s: %v", id, err)
	}
	var stdout, stderr bytes.Buffer
	if err := c.ExecStartTo(ctx, execID, &stdout, &stderr); err != nil {
		t.Fatalf("starting exec %s: %v", execID, err)
	}
	ins, err := c.ExecInspect(ctx, execID)
	if err != nil {
		t.Fatalf("inspecting exec %s: %v", execID, err)
	}
	if ins.Running {
		t.Error("an exec must not report Running once its output stream has closed")
	}
	if _, err := c.ExecInspect(ctx, "missing"); !dockerclient.IsNotFound(err) {
		t.Errorf("inspecting an unknown exec must report ErrNotFound, got %v", err)
	}
}
