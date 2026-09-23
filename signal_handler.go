package mongotest

import (
	"context"
	"fmt"

	"github.com/tophergopher/mongotest/reaper"
)

// Signal handling for this module lives in the reaper package. This file used
// to install its own handler, and every part of how it did so was wrong:
//
//   - from an init(), so importing this package changed how any binary
//     answered Ctrl-C whether or not it ever started a container;
//   - for SIGKILL, which cannot be caught, and SIGQUIT, which the Go runtime
//     uses to dump goroutine stacks;
//   - reading one signal and then letting the goroutine return, so a second
//     was unhandled;
//   - and never re-raising, so after reaping the process went on running and
//     Ctrl-C no longer terminated it.
//
// The cache of legacy connections is registered with the reaper instead, when
// the first container is cached rather than when the package loads. Signals are
// then handled once, correctly, for the whole module. See reaper's package
// documentation, and signal_handler_test.go for what keeps the old handler from
// coming back.

// registerForReaping arranges for one cached connection to be killed if the
// process is signalled, and records the handle that takes the registration
// back again.
//
// Per connection rather than once for the whole cache: reaper.Reap drops every
// registration as it runs them, so a single registration covering the cache
// would stop covering anything cached after the first explicit reap.
//
// The teardown only looks: KillMongoContainer is what removes the connection
// from the cache and unregisters it, on success and in one place, so a
// container that could not be removed stays registered and is tried again
// rather than being quietly forgotten.
func registerForReaping(tc *TestConnection) {
	id := tc.mongoContainerID
	handle := reaper.Register(id, func(context.Context) error {
		if _, live := containerCache.Load(id); !live {
			return nil // already killed, by ReapRunningContainers or by hand
		}
		return tc.KillMongoContainer()
	})
	// Under the same lock the teardown takes, because the registration above
	// is live the instant Register returns: a signal arriving between these
	// two statements has the reaper's goroutine reading this field while this
	// one writes it. Whichever wins, a teardown that ran first leaves a
	// registration the reap has already dropped, which Unregister reports as
	// gone and nothing acts on.
	tc.killMu.Lock()
	tc.reaperHandle = handle
	tc.killMu.Unlock()
}

// forgetContainer gives back everything this package is holding for a
// container that has just been removed.
//
// Both halves matter. Left in the cache, the container is killed a second time
// on a signal, on the reaper's goroutine and unsynchronised against the test
// goroutine that killed it. Left registered, it makes reaper.Unregister -- a
// linear scan, run on every mongod Stop -- longer for the rest of the process,
// and it turns reaper.Names(), which exists to say which container leaked,
// into a list of every container that was cleaned up correctly.
//
// Called with killMu held, which is what makes reading the handle safe.
func (tc *TestConnection) forgetContainer(id string) {
	containerCache.Delete(id)
	reaper.Unregister(tc.reaperHandle)
}

// ReapRunningContainers kills every container this package has started and
// cached. It is called by the reaper when the process is signalled, and can be
// called directly.
//
// Per-container failures are reported to stdout rather than returned, which is
// the behaviour this function has always had; the replacement in mongod returns
// an error naming each container that could not be removed.
func ReapRunningContainers() {
	for _, testConn := range getAllCachedConnections() {
		if testConn == nil {
			continue
		}
		// KillMongoContainer takes it out of the cache and gives its
		// registration back, so a later reap does not try again -- and one
		// that could not be removed is deliberately left in both, to be
		// tried again rather than lost.
		fmt.Println("Killing container from ReapRunningContainers")
		_ = testConn.KillMongoContainer()
	}
}
