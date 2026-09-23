// Command reaphelper starts a real MongoDB container and then waits to be
// signalled. It exists so that the reaper's signal path can be tested for what
// actually matters: that a signalled process both tears its containers down and
// still dies of the signal it was sent.
//
// Neither half can be tested from inside a test binary. Sending a signal to the
// test process fights the test runner's own handling, and the interesting
// outcome -- the process ending, with a particular wait status -- is something
// only a parent can observe. So this is a separate program, built and run by
// TestIntegrationSignalReapsAndStillDies in the reaper package.
//
// It prints one line to stdout, "container <id> <name>", once the container is
// up, and then blocks. The test reads that line, sends a signal, and checks
// both the wait status and whether the container is gone.
package main

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/tophergopher/mongotest/mongod"
)

func main() {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	opts := mongod.NewOptions()
	// The test passes a label so it can find anything this leaks.
	if value := os.Getenv("REAPHELPER_LABEL"); value != "" {
		opts = opts.WithLabel("mongotest.reaphelper", value)
	}

	c, err := mongod.Start(ctx, opts)
	if err != nil {
		fmt.Fprintln(os.Stderr, "reaphelper: cannot start a container:", err)
		os.Exit(2)
	}

	// The test is waiting on this line before it signals.
	fmt.Printf("container %s %s\n", c.ID(), c.Name())
	if err := os.Stdout.Sync(); err != nil {
		// Nothing useful to do about it; the test will time out and say so.
		fmt.Fprintln(os.Stderr, "reaphelper: cannot flush stdout:", err)
	}

	// Wait to be signalled. Reaching the end of this would mean the signal
	// never arrived, so it is a failure and not a clean exit.
	time.Sleep(5 * time.Minute)
	fmt.Fprintln(os.Stderr, "reaphelper: no signal arrived")
	os.Exit(3)
}
