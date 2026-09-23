package reaper

import (
	"bufio"
	"context"
	"errors"
	"os"
	"os/exec"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNotIgnoredDropsIgnoredSignals(t *testing.T) {
	ignored := func(s os.Signal) bool { return s == syscall.SIGINT }

	got := notIgnored(DefaultSignals(), ignored)

	assert.Equal(t, []os.Signal{syscall.SIGTERM}, got,
		"Notify on an ignored SIGINT un-ignores it, so a signal the process was started with ignored must not be listened for")
}

// ignoredHelperEnv makes this test binary act as the child in
// TestRegisteringKeepsAnIgnoredSIGINTIgnored.
const ignoredHelperEnv = "REAPER_IGNORED_SIGINT_HELPER"

// A non-interactive shell starts background jobs with SIGINT ignored. The
// reaper must not undo that: before the fix, the first Register un-ignored
// SIGINT, and a SIGINT then reaped and killed a process meant to survive it.
func TestRegisteringKeepsAnIgnoredSIGINTIgnored(t *testing.T) {
	if os.Getenv(ignoredHelperEnv) == "1" {
		Register("helper", func(context.Context) error { return nil })
		os.Stdout.WriteString("ready\n")
		// Long enough for a delivered SIGINT to have been reaped, which empties
		// the registry; short of reRaiseGrace, after which a handled SIGINT
		// would have ended the process.
		time.Sleep(time.Second)
		if len(Names()) == 0 {
			os.Stdout.WriteString("reaped\n")
		} else {
			os.Stdout.WriteString("untouched\n")
		}
		os.Exit(0)
	}

	// trap "" INT ignores SIGINT, and exec keeps an ignored disposition, which
	// is exactly how a shell starts a background job.
	cmd := exec.Command("sh", "-c", `trap "" INT; exec "$0" -test.run='^TestRegisteringKeepsAnIgnoredSIGINTIgnored$'`, os.Args[0])
	cmd.Env = append(os.Environ(), ignoredHelperEnv+"=1")
	stdout, err := cmd.StdoutPipe()
	require.NoError(t, err)
	require.NoError(t, cmd.Start())

	lines := bufio.NewReader(stdout)
	line, err := lines.ReadString('\n')
	require.NoError(t, err, "the helper prints ready once it has registered")
	require.Equal(t, "ready\n", line)
	require.NoError(t, cmd.Process.Signal(syscall.SIGINT))

	line, err = lines.ReadString('\n')
	require.NoError(t, err, "the helper reports whether the SIGINT reached the reaper")
	assert.Equal(t, "untouched\n", line,
		"the helper was started with SIGINT ignored, so the SIGINT must not have been listened for, let alone reaped")

	err = cmd.Wait()
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		t.Fatalf("the helper was started with SIGINT ignored and must survive one, but it ended with %v", exitErr)
	}
	require.NoError(t, err)
}
