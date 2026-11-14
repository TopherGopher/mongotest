// Command ignorehelper registers a teardown with the reaper and waits to be
// signalled. The reaper's tests run it under a shell that ignores a signal,
// to check that the signal stays ignored: the disposition is inherited at
// exec, so only a separate process can show it.
//
// It prints "ready <installed>" once registered, and writes a marker file if
// its teardown runs.
package main

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/tophergopher/mongotest/internal/reaper"
)

func main() {
	marker := os.Getenv("IGNOREHELPER_MARKER")

	reaper.Register("ignorehelper", func(context.Context) error {
		if marker == "" {
			return nil
		}
		return os.WriteFile(marker, []byte("reaped\n"), 0o600)
	})
	fmt.Printf("ready %t\n", reaper.Installed())

	// Getting past this means no signal arrived, which is a failure.
	time.Sleep(5 * time.Minute)
	fmt.Fprintln(os.Stderr, "ignorehelper: no signal arrived")
	os.Exit(3)
}
