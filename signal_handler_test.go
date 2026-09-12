package mongotest

import (
	"context"
	"os"
	"sync"
	"syscall"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/tophergopher/mongotest/reaper"
)

// The signal handling in this package used to be installed from an init(), for
// SIGKILL among others, handled exactly one signal, and never re-raised -- so
// Ctrl-C stopped terminating any binary that imported this package. The reaper
// package now owns signals for the whole module; these tests are what keeps the
// old handler from coming back.
//
// They need no Docker daemon: what is under test is the registration, not the
// teardown.

// The state of the reaper as this package finished loading, captured before any
// test could touch it. The lazy-registration property is only observable at
// this moment: once a test has started a container, the registration exists and
// nothing can tell whether it came from an init() or from that container.
var (
	registeredAtLoad []string
	installedAtLoad  bool
)

func TestMain(m *testing.M) {
	registeredAtLoad = reaper.Names()
	installedAtLoad = reaper.Installed()
	os.Exit(m.Run())
}

func TestImportingThisPackageInstallsNoSignalHandler(t *testing.T) {
	assert.False(t, installedAtLoad,
		"importing this package must not install a signal handler: one installed from an init() changes how every binary that imports it answers Ctrl-C, whether or not it ever starts a container, and the handler that used to be here never re-raised, so the process went on running")
	assert.Empty(t, registeredAtLoad,
		"and it must register nothing either; the legacy containers are registered when the first one is cached, which is what makes the handler lazy")
}

// resetLegacyReaper puts the package back to its just-loaded state, so that
// these tests do not depend on whether a container-starting test ran first.
func resetLegacyReaper(t *testing.T) {
	t.Helper()
	clear := func() {
		// Emptied before reaping: the registered teardown calls the legacy
		// KillMongoContainer, which dereferences a docker client that a
		// connection built by hand in these tests does not have.
		containerCache.Range(func(k, _ any) bool {
			containerCache.Delete(k)
			return true
		})
		_ = reaper.Reap(context.Background())
		legacyReaperOnce = sync.Once{}
	}
	clear()
	t.Cleanup(clear)
}

func TestCachingTheFirstContainerRegistersTheLegacyReap(t *testing.T) {
	resetLegacyReaper(t)

	cacheConnection(&TestConnection{mongoContainerID: "cafe1234"})

	assert.Contains(t, reaper.Names(), legacyReaperName,
		"a cached legacy container is torn down on a signal like any other, but by the reaper's handler rather than the broken one this package used to install")
}

func TestCachingASecondContainerDoesNotRegisterAgain(t *testing.T) {
	resetLegacyReaper(t)

	cacheConnection(&TestConnection{mongoContainerID: "cafe1234"})
	cacheConnection(&TestConnection{mongoContainerID: "cafe5678"})

	var registered int
	for _, name := range reaper.Names() {
		if name == legacyReaperName {
			registered++
		}
	}
	assert.Equal(t, 1, registered,
		"one registration covers the whole cache, because ReapRunningContainers already walks all of it; one per container would tear the same cache down repeatedly")
}

func TestNilConnectionIsNotCached(t *testing.T) {
	resetLegacyReaper(t)

	cacheConnection(nil)

	assert.NotContains(t, reaper.Names(), legacyReaperName,
		"there is nothing to reap, so nothing is registered and no handler is installed")
}

func TestTheSignalsTheOldHandlerGrabbedAreLeftAlone(t *testing.T) {
	for _, sig := range []syscall.Signal{syscall.SIGQUIT, syscall.SIGABRT, syscall.SIGKILL} {
		assert.NotContains(t, reaper.DefaultSignals(), os.Signal(sig),
			"%s must be left alone: SIGQUIT is how the Go runtime is asked to dump goroutine stacks, SIGABRT likewise, and SIGKILL cannot be caught at all -- the old handler asked for all three", sig)
	}
}
