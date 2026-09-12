package reaper_test

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/tophergopher/mongotest/dockerapi"
	"github.com/tophergopher/mongotest/dockerclient"
)

// This test needs a real Docker daemon and fails, rather than skipping, when
// there is none. The signal path is the one piece of this package that unit
// tests cannot reach, so a silent skip would leave it untested.

// TestIntegrationSignalReapsAndStillDies is the regression test for the
// implementation this package replaces, which reaped and then returned: the
// process went on running, so Ctrl-C no longer terminated it.
//
// Both halves are asserted, because either alone would have passed against the
// old code at some point: the container has to be gone, and the process has to
// have died of the signal it was sent rather than exiting on its own terms.
func TestIntegrationSignalReapsAndStillDies(t *testing.T) {
	for _, sig := range []syscall.Signal{syscall.SIGTERM, syscall.SIGINT} {
		t.Run(sig.String(), func(t *testing.T) {
			docker := liveDocker(t)
			helper := buildHelper(t)
			label := fmt.Sprintf("%s-%d", strings.ReplaceAll(t.Name(), "/", "_"), time.Now().UnixNano())

			cmd := exec.Command(helper)
			cmd.Env = append(os.Environ(), "REAPHELPER_LABEL="+label)
			stdout, err := cmd.StdoutPipe()
			require.NoError(t, err, "the test reads the container id from the helper's stdout")
			cmd.Stderr = os.Stderr
			require.NoError(t, cmd.Start(), "the helper binary must start")

			id, name := containerFromHelper(t, stdout)
			t.Cleanup(func() {
				// Whatever happens, do not leave a container behind.
				_ = docker.ContainerRemove(context.Background(), id, dockerclient.RemoveOptions{Force: true, RemoveVolumes: true})
			})

			_, err = docker.ContainerInspect(context.Background(), id)
			require.NoError(t, err, "the container the helper reported must exist before it is signalled; otherwise this test proves nothing about the reap")

			require.NoError(t, cmd.Process.Signal(sig), "sending %s to the helper must succeed", sig)

			// Generous against the worst legitimate case -- a reap that uses
			// its whole 30s budget plus the re-raise grace -- without making a
			// real failure take a minute and a half to report.
			state := waitFor(t, cmd, 60*time.Second)

			status, ok := state.Sys().(syscall.WaitStatus)
			require.True(t, ok, "this test is unix-only, where a wait status is how a signal death is observed")
			assert.True(t, status.Signaled(),
				"the process has to die of the signal, not exit on its own terms: a handler that reaps and returns leaves Ctrl-C no longer terminating, which is the bug this replaces (exit code was %d)", state.ExitCode())
			assert.Equal(t, sig, status.Signal(),
				"and it has to die of the signal it was actually sent, since that is what the shell and any supervisor will report")

			_, err = docker.ContainerInspect(context.Background(), id)
			assert.True(t, dockerclient.IsNotFound(err),
				"container %s (%s) is still there after the process was signalled, so the reap did not run or did not finish; err was %v", name, id, err)
		})
	}
}

// buildHelper compiles the helper once per test run. It is built rather than
// run through `go run`, because `go run` sits between the signal and the
// process under test and muddies both the delivery and the exit status.
func buildHelper(t *testing.T) string {
	t.Helper()
	binary := filepath.Join(t.TempDir(), "reaphelper")
	build := exec.Command("go", "build", "-o", binary, "github.com/tophergopher/mongotest/internal/reaphelper")
	build.Stderr = os.Stderr
	require.NoError(t, build.Run(), "the helper binary must build")
	return binary
}

// containerFromHelper reads the line the helper prints once its container is up.
func containerFromHelper(t *testing.T, stdout interface{ Read([]byte) (int, error) }) (id, name string) {
	t.Helper()
	type line struct {
		text string
		err  error
	}
	lines := make(chan line, 1)
	go func() {
		scanner := bufio.NewScanner(stdout)
		if scanner.Scan() {
			lines <- line{text: scanner.Text()}
			return
		}
		lines <- line{err: scanner.Err()}
	}()

	select {
	case got := <-lines:
		require.NoError(t, got.err, "reading the helper's first line must succeed")
		fields := strings.Fields(got.text)
		require.Len(t, fields, 3, "the helper prints \"container <id> <name>\"; got %q", got.text)
		return fields[1], fields[2]
	case <-time.After(4 * time.Minute):
		require.Fail(t, "the helper never reported a container", "it may be pulling the image for the first time, or the daemon may be unreachable")
		return "", ""
	}
}

// waitFor waits for the process to end, or fails the test rather than hanging.
func waitFor(t *testing.T, cmd *exec.Cmd, timeout time.Duration) *os.ProcessState {
	t.Helper()
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case err := <-done:
		var exit *exec.ExitError
		if err != nil && !errors.As(err, &exit) {
			require.NoError(t, err, "waiting for the helper must not fail for any reason other than its exit status")
		}
		return cmd.ProcessState
	case <-time.After(timeout):
		_ = cmd.Process.Kill()
		require.Fail(t, "the helper did not exit after being signalled",
			"it reaped but kept running, or the reap itself hung; either way a signalled process has to end")
		return nil
	}
}

// liveDocker returns a client for the real daemon, or fails with guidance.
func liveDocker(t *testing.T) dockerclient.Client {
	t.Helper()
	client, err := dockerapi.FromEnv()
	require.NoError(t, err, "reaper integration: cannot configure a Docker client; set DOCKER_HOST to point at a running Docker daemon")
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	t.Cleanup(cancel)
	require.NoError(t, client.Negotiate(ctx), "reaper integration: no Docker daemon reachable at %s", client.Host())
	return client
}
