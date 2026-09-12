package mongotest

import (
	"context"
	"os"
	"syscall"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
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
	}
	clear()
	t.Cleanup(clear)
}

func TestCachingTheFirstContainerRegistersTheLegacyReap(t *testing.T) {
	resetLegacyReaper(t)

	cacheConnection(&TestConnection{mongoContainerID: "cafe1234"})

	assert.Contains(t, reaper.Names(), "cafe1234",
		"a cached legacy container is torn down on a signal like any other, but by the reaper's handler rather than the broken one this package used to install")
}

func TestEachCachedContainerIsRegisteredOnce(t *testing.T) {
	resetLegacyReaper(t)

	cacheConnection(&TestConnection{mongoContainerID: "cafe1234"})
	cacheConnection(&TestConnection{mongoContainerID: "cafe5678"})

	assert.ElementsMatch(t, []string{"cafe1234", "cafe5678"}, reaper.Names(),
		"one registration per container, named by its id, so a reap tears each down exactly once and names the one that failed")
}

// reaper.Reap drops every registration, and the documentation recommends
// calling it from a TestMain. A container cached after that must still be
// registered, or everything started in the rest of the run leaks on a signal.
func TestContainersCachedAfterAReapAreStillRegistered(t *testing.T) {
	resetLegacyReaper(t)
	cacheConnection(&TestConnection{mongoContainerID: "cafe1234"})
	containerCache.Delete("cafe1234")
	require.NoError(t, reaper.Reap(context.Background()), "the explicit reap empties the registry")
	require.Empty(t, reaper.Names(), "nothing is registered immediately after a reap")

	cacheConnection(&TestConnection{mongoContainerID: "cafe5678"})

	assert.Contains(t, reaper.Names(), "cafe5678",
		"the container cached after the reap is registered too; a single registration for the whole cache would have been dropped by the reap and never replaced")
}

func TestNilConnectionIsNotCached(t *testing.T) {
	resetLegacyReaper(t)

	cacheConnection(nil)

	assert.Empty(t, reaper.Names(),
		"there is nothing to reap, so nothing is registered and no handler is installed")
}

func TestTheSignalsTheOldHandlerGrabbedAreLeftAlone(t *testing.T) {
	for _, sig := range []syscall.Signal{syscall.SIGQUIT, syscall.SIGABRT, syscall.SIGKILL} {
		assert.NotContains(t, reaper.DefaultSignals(), os.Signal(sig),
			"%s must be left alone: SIGQUIT is how the Go runtime is asked to dump goroutine stacks, SIGABRT likewise, and SIGKILL cannot be caught at all -- the old handler asked for all three", sig)
	}
}
