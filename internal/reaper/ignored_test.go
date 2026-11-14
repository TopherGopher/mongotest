package reaper_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// A non-interactive shell starts a background job with SIGINT ignored, and
// os/signal says that calling Notify for an ignored SIGINT installs a handler
// and stops it being ignored. A process that was meant to survive Ctrl-C would
// then start reaping and exiting on one, because it happened to register a
// container.
//
// This needs a real process. The disposition is inherited at exec, the test
// runner has handlers of its own, and the outcome being asserted -- that the
// process is still there afterwards -- is something only a parent can see.
// `trap ” INT` in the shell is what produces the inherited "ignored" state.
func TestIgnoredSignalsAreLeftIgnored(t *testing.T) {
	helper := buildIgnoreHelper(t)
	marker := filepath.Join(t.TempDir(), "reaped")

	// exec so that the shell is replaced: the signal has to reach the process
	// under test, not a shell that would decide for itself what to do with it.
	cmd := exec.Command("/bin/sh", "-c", `trap '' INT; exec "$0"`, helper)
	cmd.Env = append(os.Environ(), "IGNOREHELPER_MARKER="+marker)
	stdout, err := cmd.StdoutPipe()
	require.NoError(t, err, "the test reads the helper's readiness line from stdout")
	cmd.Stderr = os.Stderr
	require.NoError(t, cmd.Start(), "the helper binary must start")
	t.Cleanup(func() { _ = cmd.Process.Kill() })

	ready := firstLine(t, stdout, 2*time.Minute)
	require.Equal(t, "ready true", ready,
		"SIGTERM is not ignored here, so there is still something worth listening for and the handler has to go in; %q means the filter threw away the whole set", ready)

	require.NoError(t, cmd.Process.Signal(syscall.SIGINT), "sending SIGINT to the helper must succeed")

	// Long enough for a handler that should not exist to have reaped and
	// re-raised: the reap is immediate here, and the re-raise follows it.
	time.Sleep(2 * time.Second)
	require.NoError(t, cmd.Process.Signal(syscall.Signal(0)),
		"the process was started with SIGINT ignored and must still be running: registering a container must not change how a process answers a signal it never asked about, which is the same bug as installing a handler from an init()")
	assert.NoFileExists(t, marker,
		"and nothing may have been torn down either, since the signal that would have triggered it was one this process was told to ignore")

	// The other half: a signal that was not ignored still works, so the
	// filter has not simply disabled the package.
	require.NoError(t, cmd.Process.Signal(syscall.SIGTERM), "sending SIGTERM to the helper must succeed")
	state := waitFor(t, cmd, 60*time.Second)

	status, ok := state.Sys().(syscall.WaitStatus)
	require.True(t, ok, "this test is unix-only, where a wait status is how a signal death is observed")
	assert.True(t, status.Signaled(), "SIGTERM was not ignored, so the handler installed for it has to reap and then let the process die of it (exit code was %d)", state.ExitCode())
	assert.Equal(t, syscall.SIGTERM, status.Signal(), "and it has to die of the signal it was sent")
	assert.FileExists(t, marker, "the teardown registered with the reaper has to have run for the signal that was not ignored")
}

// buildIgnoreHelper compiles the helper for this test.
func buildIgnoreHelper(t *testing.T) string {
	t.Helper()
	binary := filepath.Join(t.TempDir(), "ignorehelper")
	build := exec.Command("go", "build", "-o", binary, "github.com/tophergopher/mongotest/internal/ignorehelper")
	build.Stderr = os.Stderr
	require.NoError(t, build.Run(), "the helper binary must build")
	return binary
}
