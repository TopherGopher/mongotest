package reaper

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"github.com/tophergopher/mongotest/dockerclient"
)

// DefaultReapTimeout bounds a reap run from the signal handler. A process that
// is being asked to die should not hang indefinitely because one container will
// not remove, and thirty seconds is long enough for a daemon that is merely
// busy.
const DefaultReapTimeout = 30 * time.Second

// reRaiseGrace is how long the handler waits for a re-raised signal to be
// delivered before giving up and exiting itself.
const reRaiseGrace = 5 * time.Second

// ErrReap is matched by the error a reap returns when any teardown failed. The
// error wraps every individual failure, so errors.Is also matches those.
var ErrReap = errors.New("one or more teardowns failed")

// Handle identifies one registration. Unregister takes it back.
//
// Handles are never reused, so a handle kept by something that has already
// torn itself down cannot remove a registration that came afterwards.
type Handle int64

// registry is the process-wide set of teardowns. It is global on purpose: a
// signal arrives once for the process, not once per caller, so there is exactly
// one thing that can act on it.
//
// A mutex and a slice rather than a sync.Map, for two reasons. Teardown order
// matters and a sync.Map has none to offer. And the access pattern is a handful
// of writes around container start and stop, not the read-mostly hot path
// sync.Map is for; a mutex here is both faster and easier to reason about.
var registry struct {
	mu      sync.Mutex
	entries []entry
	next    Handle
	// handler is non-nil once a signal handler is installed.
	handler chan os.Signal
	logger  dockerclient.Logger
}

// entry is one registered teardown.
type entry struct {
	handle Handle
	// name identifies the thing being torn down, for the message a failure
	// produces. It is all a reader gets from a process that is dying.
	name string
	tear func(context.Context) error
}

// DefaultSignals returns the signals a lazily installed handler listens for.
//
// Only the two that a caller means as "shut down": SIGINT from Ctrl-C, and
// SIGTERM from a CI runner or a container runtime. Not SIGKILL, which cannot be
// caught at all and whose presence in a handler only suggests a guarantee that
// does not exist. Not SIGQUIT either, because the Go runtime dumps goroutine
// stacks on it and intercepting that would trade a debugging tool for a
// container.
func DefaultSignals() []os.Signal {
	return []os.Signal{syscall.SIGINT, syscall.SIGTERM}
}

// SetLogger sends what a reap does to a logger. The default discards it.
//
// Worth setting: a reap runs while a process is on its way out, where a
// returned error has nowhere to go and often nobody to read it.
func SetLogger(l dockerclient.Logger) {
	registry.mu.Lock()
	defer registry.mu.Unlock()
	registry.logger = l
}

// Register records a teardown to run when the process is signalled, or when
// Reap is called, and returns the handle that takes it back.
//
// The first call also installs the signal handler, which is why importing this
// package changes nothing on its own. That matters: a handler installed from a
// package initialiser changes how every binary that imports it responds to
// Ctrl-C, whether or not it ever starts a container.
//
// tear is called with a context that may already be close to its deadline, so
// it should do the one thing it must and not retry at length.
func Register(name string, tear func(context.Context) error) Handle {
	registry.mu.Lock()
	defer registry.mu.Unlock()
	registry.next++
	registry.entries = append(registry.entries, entry{handle: registry.next, name: name, tear: tear})
	if registry.handler == nil {
		installLocked(DefaultSignals())
	}
	return registry.next
}

// Unregister takes a registration back, and reports whether it was still
// there. Something that has torn itself down calls this so that a reap does not
// do it again.
func Unregister(h Handle) bool {
	registry.mu.Lock()
	defer registry.mu.Unlock()
	for i, e := range registry.entries {
		if e.handle == h {
			registry.entries = append(registry.entries[:i], registry.entries[i+1:]...)
			return true
		}
	}
	return false
}

// Names returns what is registered, in registration order, which is the order
// the things were started in. It is for diagnostics: a test that leaks a
// container can say which.
func Names() []string {
	registry.mu.Lock()
	defer registry.mu.Unlock()
	out := make([]string, 0, len(registry.entries))
	for _, e := range registry.entries {
		out = append(out, e.name)
	}
	return out
}

// Reap runs every registered teardown and clears the registry.
//
// It is exported so that teardown never depends on a signal arriving: a caller
// can defer it, a test can call it, and the signal handler is then a thin thing
// that calls the same function everything else does.
//
// Every teardown runs even when an earlier one fails, because the containers
// left behind are the thing being prevented; the errors are joined, so
// errors.Is finds each of them as well as ErrReap. A teardown is taken off the
// registry whether or not it succeeded: retrying a removal that the daemon
// refused is not this function's job, and doing it twice risks a second reap
// removing something a later caller started.
func Reap(ctx context.Context) error {
	registry.mu.Lock()
	entries := registry.entries
	registry.entries = nil
	logger := registry.logger
	registry.mu.Unlock()

	if len(entries) == 0 {
		return nil
	}
	if logger != nil {
		logger.Info("reaping registered teardowns", "count", len(entries))
	}

	// Reverse registration order: later things are the more likely to depend
	// on earlier ones.
	var errs []error
	for i := len(entries) - 1; i >= 0; i-- {
		e := entries[i]
		if err := e.tear(ctx); err != nil {
			wrapped := fmt.Errorf("reaper: cannot tear down %s: %w", e.name, err)
			errs = append(errs, wrapped)
			if logger != nil {
				logger.Error("teardown failed", "name", e.name, "error", err)
			}
			continue
		}
		if logger != nil {
			logger.Debug("torn down", "name", e.name)
		}
	}
	if len(errs) == 0 {
		return nil
	}
	return &ReapError{Errs: errs}
}

// ReapError reports that at least one teardown failed. It wraps every
// individual failure, so errors.Is matches both ErrReap and any of them.
type ReapError struct {
	// Errs holds one error per teardown that failed, each naming what it was.
	Errs []error
}

func (e *ReapError) Error() string {
	if len(e.Errs) == 1 {
		return e.Errs[0].Error()
	}
	return fmt.Sprintf("reaper: %d of the registered teardowns failed: %v", len(e.Errs), errors.Join(e.Errs...))
}

// Unwrap returns the individual failures, so errors.Is reaches each of them.
func (e *ReapError) Unwrap() []error { return e.Errs }

// Is matches ErrReap, so a caller can branch on "teardown had problems"
// without knowing what was registered.
func (e *ReapError) Is(target error) bool { return target == ErrReap }

// Install installs a signal handler for the given signals, or for
// DefaultSignals when none are given. Register does this on its own, so this is
// for a caller who wants the handler in place before the first container
// starts, or who wants a different set of signals.
//
// Installing twice does nothing the second time.
func Install(signals ...os.Signal) {
	if len(signals) == 0 {
		signals = DefaultSignals()
	}
	registry.mu.Lock()
	defer registry.mu.Unlock()
	if registry.handler != nil {
		return
	}
	installLocked(signals)
}

// Installed reports whether a signal handler is in place.
func Installed() bool {
	registry.mu.Lock()
	defer registry.mu.Unlock()
	return registry.handler != nil
}

// installLocked starts the handler. The registry lock is held.
func installLocked(signals []os.Signal) {
	ch := make(chan os.Signal, 1)
	registry.handler = ch
	signal.Notify(ch, signals...)
	go wait(ch, signals)
}

// wait reaps and then lets the signal do what it was going to do.
//
// The order is deliberate. The handler is removed first, so a second signal
// during teardown kills the process at once: someone pressing Ctrl-C twice has
// said they are done waiting, and honouring that matters more than the
// containers. Then the reap runs under its own deadline. Then the signal is
// re-raised, so the process dies of what it was sent rather than exiting
// quietly -- a handler that reaps and returns leaves Ctrl-C not terminating,
// which is exactly the bug this replaces.
func wait(ch chan os.Signal, signals []os.Signal) {
	sig, ok := <-ch
	if !ok {
		return // stopped, which only the tests do
	}
	signal.Stop(ch)
	// Only the signal that arrived. signal.Reset is process-wide -- it cancels
	// every package's Notify for whatever it is given -- and resetting the
	// whole set would take another library's SIGTERM handling away because a
	// SIGINT happened to arrive here. One is all the re-raise below needs.
	signal.Reset(sig)

	ctx, cancel := context.WithTimeout(context.Background(), DefaultReapTimeout)
	defer cancel()
	if err := Reap(ctx); err != nil {
		registry.mu.Lock()
		logger := registry.logger
		registry.mu.Unlock()
		if logger != nil {
			logger.Error("reap on signal did not finish cleanly", "signal", sig.String(), "error", err)
		} else {
			// There is nobody else to tell, and a container left running is
			// worth a line on stderr.
			fmt.Fprintf(os.Stderr, "reaper: %v\n", err)
		}
	}

	reRaise(sig)
}

// reRaise sends the signal to this process again, now that the default
// disposition is back, and makes sure the process ends either way.
func reRaise(sig os.Signal) {
	if p, err := os.FindProcess(os.Getpid()); err == nil {
		if err := p.Signal(sig); err == nil {
			// Delivery is asynchronous; give it a moment to end the process.
			time.Sleep(reRaiseGrace)
		}
	}
	// Re-raising did not end us: exit the way a shell reports a signal death,
	// so that whatever is watching still sees what happened.
	os.Exit(exitCodeFor(sig))
}

// exitCodeFor gives the conventional exit status for dying of a signal, which
// is what a shell reports in $?.
func exitCodeFor(sig os.Signal) int {
	const signalExitBase = 128
	if s, ok := sig.(syscall.Signal); ok {
		return signalExitBase + int(s)
	}
	return 1
}

// stopForTest empties the registry and removes any handler. It exists for the
// tests in this package, which have to share one global registry and so have to
// clean up after each other.
func stopForTest() {
	registry.mu.Lock()
	ch := registry.handler
	registry.handler = nil
	registry.entries = nil
	registry.logger = nil
	registry.mu.Unlock()
	if ch != nil {
		signal.Stop(ch)
		close(ch)
	}
}
