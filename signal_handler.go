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
// process is signalled.
//
// Per connection rather than once for the whole cache: reaper.Reap drops every
// registration as it runs them, so a single registration covering the cache
// would stop covering anything cached after the first explicit reap. The
// teardown takes the connection out of the cache as it goes, so that a reap and
// a direct ReapRunningContainers cannot both kill the same container.
func registerForReaping(tc *TestConnection) {
	id := tc.mongoContainerID
	reaper.Register(id, func(context.Context) error {
		if _, live := containerCache.LoadAndDelete(id); !live {
			return nil // already killed, by ReapRunningContainers or by hand
		}
		return tc.KillMongoContainer()
	})
}

// ReapRunningContainers kills every container this package has started and
// cached. It is called by the reaper when the process is signalled, and can be
// called directly.
//
// Per-container failures are reported to stdout rather than returned, which is
// the behaviour this function has always had; the replacement in mongod returns
// an error naming each container that could not be removed.
func ReapRunningContainers() {
	for id, testConn := range getAllCachedConnections() {
		if testConn == nil {
			continue
		}
		// Taken out of the cache as it is killed, so that a later reap does
		// not try to kill it again.
		containerCache.Delete(id)
		fmt.Println("Killing container from ReapRunningContainers")
		_ = testConn.KillMongoContainer()
	}
}
