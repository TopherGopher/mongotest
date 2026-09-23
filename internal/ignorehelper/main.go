// Command ignorehelper registers a teardown with the reaper and then waits to
// be signalled. It exists so that one rule can be tested for real: a signal
// this process was started with ignored has to stay ignored.
//
// That cannot be tested from inside a test binary. The disposition is
// inherited at exec and the test runner installs handlers of its own, so the
// only way to observe it is to start a process under a shell that ignores the
// signal, send it, and watch whether the process is still there.
//
// It prints "ready <installed>" once it has registered, writes a marker file
// when its teardown runs, and blocks. Reaching the end of the wait means no
// signal ever arrived, which is a failure rather than a clean exit.
package main

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/tophergopher/mongotest/reaper"
)

func main() {
	marker := os.Getenv("IGNOREHELPER_MARKER")

	reaper.Register("ignorehelper", func(context.Context) error {
		if marker == "" {
			return nil
		}
		// The test reads this to tell a reap apart from a process that merely
		// died of the signal without tearing anything down.
		return os.WriteFile(marker, []byte("reaped\n"), 0o600)
	})

	// The test is waiting on this line before it signals. Whether a handler
	// went in is half of what is being asserted: with SIGINT ignored and
	// SIGTERM not, there is still something to listen for.
	// os.Stdout is unbuffered in Go, so the line is on its way as it is
	// written and there is nothing to flush.
	fmt.Printf("ready %t\n", reaper.Installed())

	time.Sleep(5 * time.Minute)
	fmt.Fprintln(os.Stderr, "ignorehelper: no signal arrived")
	os.Exit(3)
}
