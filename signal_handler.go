package mongotest

import (
	"context"
	"fmt"
	"sync"

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
const legacyReaperName = "mongotest legacy containers"

// legacyReaperOnce registers the cache with the reaper on the first container,
// which is the same lazy rule the reaper itself follows.
var legacyReaperOnce sync.Once

// registerLegacyReaper arranges for the cached connections to be torn down if
// the process is signalled. One registration covers the whole cache, because
// ReapRunningContainers already walks all of it.
func registerLegacyReaper() {
	legacyReaperOnce.Do(func() {
		reaper.Register(legacyReaperName, func(context.Context) error {
			ReapRunningContainers()
			return nil
		})
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
	cachedConnections := getAllCachedConnections()
	for _, testConn := range cachedConnections {
		if testConn == nil {
			continue
		}
		fmt.Println("Killing container from ReapRunningContainers")
		_ = testConn.KillMongoContainer()
	}
}
