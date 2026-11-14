package reaper

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"slices"
	"sync"
	"syscall"
	"time"

	"github.com/tophergopher/mongotest/dockerclient"
)

// DefaultReapTimeout bounds the reap a signal triggers, so one container
// that will not remove cannot keep a dying process alive.
const DefaultReapTimeout = 30 * time.Second

// reRaiseGrace is how long to wait for a re-raised signal to end the
// process before exiting directly.
const reRaiseGrace = 5 * time.Second

// runningPoll is how often the signal path looks again while a Reap started
// elsewhere is still running its teardowns.
const runningPoll = 10 * time.Millisecond

// ErrReap is matched by the error Reap returns when any teardown failed.
var ErrReap = errors.New("one or more teardowns failed")

// Handle identifies one registration. Handles are never reused.
type Handle int64

// entry is one registered teardown. The name is for error messages.
type entry struct {
	handle Handle
	name   string
	tear   func(context.Context) error
}

// registry is the process-wide set of teardowns. It is global because a
// signal arrives once for the process, not once per caller.
var registry struct {
	mu      sync.Mutex
	entries []entry // in registration order
	// running counts teardowns a Reap has taken and not yet finished.
	running int
	next    Handle
	// handler is non-nil once a signal handler is installed.
	handler chan os.Signal
	logger  dockerclient.Logger
}

// DefaultSignals returns the signals handled by default: SIGINT and SIGTERM.
// SIGKILL cannot be caught, and SIGQUIT is left for Go's stack dump.
func DefaultSignals() []os.Signal {
	return []os.Signal{syscall.SIGINT, syscall.SIGTERM}
}

// SetLogger sends what a reap does to a logger. It is worth setting: a reap
// on a signal has nowhere else to report a failure but stderr.
func SetLogger(l dockerclient.Logger) {
	registry.mu.Lock()
	defer registry.mu.Unlock()
	registry.logger = l
}

// logger returns the registry's logger, or one that discards.
func logger() dockerclient.Logger {
	registry.mu.Lock()
	defer registry.mu.Unlock()
	if registry.logger == nil {
		return dockerclient.NopLogger()
	}
	return registry.logger
}

// Register records a teardown to run on a signal or a Reap, and returns the
// handle that takes it back. The first call installs the signal handler, so
// importing this package alone changes nothing.
//
// name appears in the error if the teardown fails. The teardown's context may
// be close to its deadline, so it should not retry at length.
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
// there. Call it once the thing has been torn down some other way.
func Unregister(h Handle) bool {
	registry.mu.Lock()
	defer registry.mu.Unlock()
	i := slices.IndexFunc(registry.entries, func(e entry) bool { return e.handle == h })
	if i < 0 {
		return false
	}
	registry.entries = slices.Delete(registry.entries, i, i+1)
	return true
}

// Names returns what is registered, oldest first. It is for diagnostics: a
// test that leaks a container can say which.
func Names() []string {
	registry.mu.Lock()
	defer registry.mu.Unlock()
	names := make([]string, 0, len(registry.entries))
	for _, e := range registry.entries {
		names = append(names, e.name)
	}
	return names
}

// Reap runs every registered teardown, newest first, and empties the
// registry. It needs no signal: call it at the end of a TestMain.
//
// Every teardown runs even if one fails. The error is a [ReapError] naming
// each failure. A failed teardown is not kept for a second attempt.
func Reap(ctx context.Context) error {
	entries := takeEntries()
	if len(entries) == 0 {
		return nil
	}
	log := logger()
	log.Info("reaping registered teardowns", "count", len(entries))

	var errs []error
	for _, e := range slices.Backward(entries) {
		err := e.tear(ctx)
		finishOne()
		if err != nil {
			errs = append(errs, fmt.Errorf("reaper: cannot tear down %s: %w", e.name, err))
			log.Error("teardown failed", "name", e.name, "error", err)
			continue
		}
		log.Debug("torn down", "name", e.name)
	}
	if len(errs) == 0 {
		return nil
	}
	return &ReapError{Errs: errs}
}

// takeEntries empties the registry and returns what was in it, counting
// each entry as running until finishOne is called for it.
func takeEntries() []entry {
	registry.mu.Lock()
	defer registry.mu.Unlock()
	entries := registry.entries
	registry.entries = nil
	registry.running += len(entries)
	return entries
}

// finishOne records that a teardown taken by takeEntries has returned.
func finishOne() {
	registry.mu.Lock()
	defer registry.mu.Unlock()
	registry.running--
}

// outstanding returns how many teardowns are registered, and how many a
// Reap is running right now.
func outstanding() (registered, running int) {
	registry.mu.Lock()
	defer registry.mu.Unlock()
	return len(registry.entries), registry.running
}

// ReapError reports that at least one teardown failed. It matches ErrReap
// and each individual failure.
type ReapError struct {
	Errs []error // one per failed teardown, each naming what it was
}

func (e *ReapError) Error() string {
	if len(e.Errs) == 1 {
		return e.Errs[0].Error()
	}
	return fmt.Sprintf("reaper: %d teardowns failed:\n%v", len(e.Errs), errors.Join(e.Errs...))
}

func (e *ReapError) Unwrap() []error { return e.Errs }

func (e *ReapError) Is(target error) bool { return target == ErrReap }

// Install installs the signal handler for the given signals, or for
// DefaultSignals if none are given. Call it only to choose the signals, and
// before the first Register: once a handler is in place, Install does nothing.
func Install(signals ...os.Signal) {
	if len(signals) == 0 {
		signals = DefaultSignals()
	}
	registry.mu.Lock()
	defer registry.mu.Unlock()
	if registry.handler == nil {
		installLocked(signals)
	}
}

// Installed reports whether a signal handler is in place.
func Installed() bool {
	registry.mu.Lock()
	defer registry.mu.Unlock()
	return registry.handler != nil
}

// installLocked starts the handler. The caller holds the registry lock.
//
// A signal the process was started with ignored is left ignored: listening
// for it would turn a signal the process was meant to survive, such as
// SIGINT in a shell's background job, into a reap and an exit.
func installLocked(signals []os.Signal) {
	active := filterSignals(signals, signal.Ignored)
	if len(active) == 0 {
		return // nothing to listen for; the next Register tries again
	}
	ch := make(chan os.Signal, 1)
	registry.handler = ch
	signal.Notify(ch, active...)
	go reapOnSignal(ch)
}

// filterSignals returns the signals that are not ignored.
func filterSignals(signals []os.Signal, ignored func(os.Signal) bool) []os.Signal {
	active := make([]os.Signal, 0, len(signals))
	for _, sig := range signals {
		if !ignored(sig) {
			active = append(active, sig)
		}
	}
	return active
}

// reapOnSignal waits for a signal, reaps, and then lets the signal end the
// process as it would have without a handler.
func reapOnSignal(ch chan os.Signal) {
	sig, ok := <-ch
	if !ok {
		return // the handler was removed, which only the tests do
	}
	// Stop handling first, so a second Ctrl-C kills at once. Only this
	// signal is reset: Reset undoes every package's Notify for it.
	signal.Stop(ch)
	signal.Reset(sig)

	ctx, cancel := context.WithTimeout(context.Background(), DefaultReapTimeout)
	defer cancel()
	if err := reapUntilEmpty(ctx); err != nil {
		logger().Error("reap on signal did not finish cleanly", "signal", sig.String(), "error", err)
		fmt.Fprintf(os.Stderr, "reaper: %v\n", err)
	}
	reRaise(sig)
}

// reapUntilEmpty reaps until nothing is registered and no teardown is still
// running, or the context ends.
//
// One pass is not enough. A signal does not stop the rest of the process: a
// parallel test can register a new container while the first pass removes
// others, and a Reap that a TestMain started may still be part-way through.
func reapUntilEmpty(ctx context.Context) error {
	var errs []error
	for ctx.Err() == nil {
		registered, running := outstanding()
		switch {
		case registered > 0:
			if err := Reap(ctx); err != nil {
				errs = append(errs, err)
			}
		case running > 0:
			// Someone else's Reap holds what is left. Wait for it.
			select {
			case <-ctx.Done():
			case <-time.After(runningPoll):
			}
		default:
			return errors.Join(errs...)
		}
	}
	return errors.Join(errs...)
}

// reRaise sends the signal to this process again, now that nothing handles
// it, so the process dies of what it was sent. If that somehow does not end
// it, it exits with the status a shell reports for a signal death.
func reRaise(sig os.Signal) {
	if p, err := os.FindProcess(os.Getpid()); err == nil {
		if err := p.Signal(sig); err == nil {
			time.Sleep(reRaiseGrace) // delivery is asynchronous
		}
	}
	os.Exit(exitCodeFor(sig))
}

// exitCodeFor returns the exit status a shell reports for dying of a signal.
func exitCodeFor(sig os.Signal) int {
	const signalExitBase = 128
	if s, ok := sig.(syscall.Signal); ok {
		return signalExitBase + int(s)
	}
	return 1
}

// stopForTest empties the registry and removes any handler, because the
// tests in this package share the one global registry.
func stopForTest() {
	registry.mu.Lock()
	ch := registry.handler
	registry.handler = nil
	registry.entries = nil
	registry.running = 0
	registry.logger = nil
	registry.mu.Unlock()
	if ch != nil {
		signal.Stop(ch)
		close(ch)
	}
}
