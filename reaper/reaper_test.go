package reaper

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// resetForTest empties the registry and stops any handler, so that one test
// cannot see another's registrations. The registry is deliberately global --
// that is the whole point of a reaper, since a signal arrives once for the
// process and not once per caller -- so tests have to clean up after
// themselves rather than getting a fresh instance each.
func resetForTest(t *testing.T) {
	t.Helper()
	stopForTest()
	t.Cleanup(stopForTest)
}

func TestReapRunsEveryTeardown(t *testing.T) {
	resetForTest(t)
	var mu sync.Mutex
	var torn []string
	tear := func(name string) func(context.Context) error {
		return func(context.Context) error {
			mu.Lock()
			defer mu.Unlock()
			torn = append(torn, name)
			return nil
		}
	}
	Register("first", tear("first"))
	Register("second", tear("second"))
	Register("third", tear("third"))

	require.NoError(t, Reap(context.Background()), "every teardown succeeds, so the reap does")

	assert.Equal(t, []string{"third", "second", "first"}, torn,
		"teardown runs in reverse registration order, because later things are more likely to depend on earlier ones")
	assert.Empty(t, Names(), "a reaped registration is gone; leaving it would tear the same container down twice")
}

func TestReapIsExplicitAndNeedsNoSignal(t *testing.T) {
	resetForTest(t)
	reaped := false
	Register("container", func(context.Context) error { reaped = true; return nil })

	require.NoError(t, Reap(context.Background()), "Reap is a plain function call")

	assert.True(t, reaped, "the whole point of exporting Reap is that a caller can tear everything down without waiting for a signal, which is also what makes the signal path testable")
}

func TestReapReportsEveryFailureAndStillRunsTheRest(t *testing.T) {
	resetForTest(t)
	first := errors.New("first cannot be removed")
	second := errors.New("second cannot be removed")
	ran := 0
	Register("ok-before", func(context.Context) error { ran++; return nil })
	Register("fails-one", func(context.Context) error { ran++; return first })
	Register("ok-between", func(context.Context) error { ran++; return nil })
	Register("fails-two", func(context.Context) error { ran++; return second })

	err := Reap(context.Background())

	require.Error(t, err, "two teardowns failed, so the reap did")
	assert.ErrorIs(t, err, first, "the first failure is reported")
	assert.ErrorIs(t, err, second, "and so is the second; a reaper that stopped at the first would leak everything after it")
	assert.Equal(t, 4, ran, "every teardown runs regardless, because the containers left behind are the thing being prevented")
}

func TestReapNamesWhatFailed(t *testing.T) {
	resetForTest(t)
	Register("mongotest-1a2b3c4d", func(context.Context) error { return errors.New("device or resource busy") })

	err := Reap(context.Background())

	require.Error(t, err, "the teardown failed")
	assert.Contains(t, err.Error(), "mongotest-1a2b3c4d",
		"a reap happens while a process is dying, so the message is all the reader gets; it has to say which registration failed")
	assert.Contains(t, err.Error(), "device or resource busy", "and why")
}

func TestReapStopsAtTheContextDeadline(t *testing.T) {
	resetForTest(t)
	released := make(chan struct{})
	t.Cleanup(func() { close(released) })
	Register("hangs", func(ctx context.Context) error {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-released:
			return nil
		}
	})

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	started := time.Now()
	err := Reap(ctx)

	require.Error(t, err, "the teardown never finished, so the reap reports it rather than pretending")
	assert.ErrorIs(t, err, context.DeadlineExceeded, "the budget is what ended it")
	assert.Less(t, time.Since(started), 5*time.Second,
		"a reaper that can be hung by one stuck container is worse than none: the signal path gives it a deadline for exactly this reason")
}

func TestUnregisterKeepsAReapFromTouchingIt(t *testing.T) {
	resetForTest(t)
	torn := false
	handle := Register("stopped-already", func(context.Context) error { torn = true; return nil })

	assert.True(t, Unregister(handle), "the handle was registered, so unregistering it reports that it did something")
	require.NoError(t, Reap(context.Background()), "there is nothing left to reap")

	assert.False(t, torn, "a container its owner already stopped must not be torn down again; the second removal would be a 404 at best and another caller's container at worst")
	assert.False(t, Unregister(handle), "unregistering twice reports that the second call found nothing, which is how a double Stop stays silent")
}

func TestNamesReportsWhatWouldBeReaped(t *testing.T) {
	resetForTest(t)
	Register("first", func(context.Context) error { return nil })
	second := Register("second", func(context.Context) error { return nil })

	assert.Equal(t, []string{"first", "second"}, Names(), "Names is in registration order, because that is the order they were started in")

	Unregister(second)
	assert.Equal(t, []string{"first"}, Names(), "and it tracks what is actually still registered, which is what makes a leak diagnosable")
}

func TestHandlesAreNotReused(t *testing.T) {
	resetForTest(t)
	first := Register("first", func(context.Context) error { return nil })
	Unregister(first)
	second := Register("second", func(context.Context) error { return nil })

	assert.NotEqual(t, first, second,
		"handles are never reused, so a stale Unregister from a container that already stopped cannot remove a different container that started afterwards")
}

func TestRegisterInstallsTheHandlerLazilyAndOnlyOnce(t *testing.T) {
	resetForTest(t)
	assert.False(t, Installed(), "nothing is installed until something is registered: importing this package must not change how a binary responds to Ctrl-C, which is the bug in the implementation it replaces")

	Register("first", func(context.Context) error { return nil })
	assert.True(t, Installed(), "the first registration installs the handler, because only then is there anything to reap")

	Register("second", func(context.Context) error { return nil })
	assert.True(t, Installed(), "a second registration does not install a second handler")
}

func TestDefaultSignalsAreOnlyTheOnesThatCanBeHandledMeaningfully(t *testing.T) {
	signals := DefaultSignals()

	assert.Contains(t, signals, os.Signal(syscall.SIGINT), "SIGINT is Ctrl-C, the common case")
	assert.Contains(t, signals, os.Signal(syscall.SIGTERM), "SIGTERM is what a CI runner and a container runtime send")
	assert.NotContains(t, signals, os.Signal(syscall.SIGKILL),
		"SIGKILL cannot be caught at all; listening for it is cargo cult and suggests a guarantee that does not exist")
	assert.NotContains(t, signals, os.Signal(syscall.SIGQUIT),
		"the Go runtime dumps goroutine stacks on SIGQUIT, and intercepting it would take away a debugging tool to save a container")
}

func TestRegisterIsSafeUnderConcurrentUse(t *testing.T) {
	resetForTest(t)
	const workers = 16
	var wg sync.WaitGroup
	var mu sync.Mutex
	torn := map[string]bool{}

	for i := range workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			name := fmt.Sprintf("container-%02d", i)
			handle := Register(name, func(context.Context) error {
				mu.Lock()
				defer mu.Unlock()
				torn[name] = true
				return nil
			})
			// Half of them stop normally, as a passing test would.
			if i%2 == 0 {
				Unregister(handle)
			}
		}()
	}
	wg.Wait()

	require.NoError(t, Reap(context.Background()), "the reap succeeds")
	mu.Lock()
	defer mu.Unlock()
	assert.Len(t, torn, workers/2, "exactly the registrations nobody unregistered were torn down; parallel tests register and unregister from many goroutines at once, so the registry has to be correct under that and not merely race-free")
}

func TestReapWithNothingRegisteredIsNil(t *testing.T) {
	resetForTest(t)

	assert.NoError(t, Reap(context.Background()), "a process that started no containers has nothing to tear down, and a deferred reap should not have to check first")
}
