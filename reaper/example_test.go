package reaper_test

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"

	"github.com/tophergopher/mongotest/reaper"
)

// The registry is process-wide, so every example here gives back what it
// registers. A real caller does the same, by unregistering when it stops the
// thing itself or by letting a reap take it.

func ExampleRegister() {
	handle := reaper.Register("mongotest-1a2b3c4d", func(context.Context) error {
		fmt.Println("tearing down mongotest-1a2b3c4d")
		return nil
	})
	defer reaper.Unregister(handle)

	fmt.Println("registered:", reaper.Names())
	// Output: registered: [mongotest-1a2b3c4d]
}

func ExampleUnregister() {
	handle := reaper.Register("stopped-by-its-owner", func(context.Context) error {
		fmt.Println("this must not run")
		return nil
	})

	// What a Stop method does: the thing is already gone, so a reap must not
	// try to remove it again.
	fmt.Println("was registered:", reaper.Unregister(handle))
	fmt.Println("and again:", reaper.Unregister(handle))

	if err := reaper.Reap(context.Background()); err != nil {
		fmt.Println("reap:", err)
	}
	// Output:
	// was registered: true
	// and again: false
}

func ExampleReap() {
	reaper.Register("first", func(context.Context) error {
		fmt.Println("tearing down first")
		return nil
	})
	reaper.Register("second", func(context.Context) error {
		fmt.Println("tearing down second")
		return nil
	})

	// No signal involved: this is what a TestMain defers.
	if err := reaper.Reap(context.Background()); err != nil {
		fmt.Println("reap:", err)
	}
	fmt.Println("left registered:", reaper.Names())
	// Reverse registration order, because later things are the more likely to
	// depend on earlier ones.
	// Output:
	// tearing down second
	// tearing down first
	// left registered: []
}

func ExampleReap_oneFails() {
	reaper.Register("removes-cleanly", func(context.Context) error { return nil })
	reaper.Register("wont-remove", func(context.Context) error {
		return errors.New("device or resource busy")
	})

	err := reaper.Reap(context.Background())

	// Every teardown still ran; the failure is reported rather than hidden,
	// and names what is still there.
	fmt.Println(err)
	fmt.Println("matches ErrReap:", errors.Is(err, reaper.ErrReap))
	// Output:
	// reaper: cannot tear down wont-remove: device or resource busy
	// matches ErrReap: true
}

func ExampleNames() {
	first := reaper.Register("mongotest-aaaa1111", func(context.Context) error { return nil })
	reaper.Register("mongotest-bbbb2222", func(context.Context) error { return nil })
	defer reaper.Reap(context.Background())

	// Registration order, which is the order the things were started in.
	fmt.Println(reaper.Names())
	reaper.Unregister(first)
	fmt.Println(reaper.Names())
	// Output:
	// [mongotest-aaaa1111 mongotest-bbbb2222]
	// [mongotest-bbbb2222]
}

func ExampleInstall() {
	// Register installs the handler on its own, so this is only for a caller
	// who wants it in place first, or who wants different signals.
	reaper.Install()

	fmt.Println("handler in place:", reaper.Installed())
	// Output: handler in place: true
}

func ExampleInstalled() {
	// Importing this package installs nothing; the first Register does it. A
	// handler installed from a package initialiser would change how every
	// binary that imports it responds to Ctrl-C.
	reaper.Install()

	fmt.Println(reaper.Installed())
	// Output: true
}

func ExampleSetLogger() {
	// Worth setting: a reap runs while the process is on its way out, where a
	// returned error often has nobody to read it.
	reaper.SetLogger(slog.New(slog.NewTextHandler(os.Stdout, nil)))
	defer reaper.SetLogger(nil)

	handle := reaper.Register("mongotest-1a2b3c4d", func(context.Context) error { return nil })
	fmt.Println("registered:", reaper.Names())
	reaper.Unregister(handle)
	// Output: registered: [mongotest-1a2b3c4d]
}

func ExampleDefaultSignals() {
	for _, sig := range reaper.DefaultSignals() {
		fmt.Println(sig)
	}
	// SIGKILL is absent because it cannot be caught, and SIGQUIT because the Go
	// runtime dumps goroutine stacks on it.
	// Output:
	// interrupt
	// terminated
}

func ExampleReapError() {
	reaper.Register("first", func(context.Context) error { return errors.New("busy") })
	reaper.Register("second", func(context.Context) error { return errors.New("in use") })

	err := reaper.Reap(context.Background())

	var reapErr *reaper.ReapError
	if errors.As(err, &reapErr) {
		fmt.Println("teardowns that failed:", len(reapErr.Errs))
	}
	// Output: teardowns that failed: 2
}

func ExampleDefaultReapTimeout() {
	// What the signal handler gives a reap. A process being asked to die should
	// not hang forever because one container will not remove.
	fmt.Println(reaper.DefaultReapTimeout)
	// Output: 30s
}
