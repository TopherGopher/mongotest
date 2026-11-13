package mongotest

import (
	"context"
	"fmt"

	"github.com/tophergopher/mongotest/reaper"
)

// Signals are handled by the reaper package. Each cached connection is
// registered with it when cached, so nothing here installs a handler.

// registerForReaping arranges for a cached connection's container to be
// killed if the process is signalled.
func registerForReaping(tc *TestConnection) {
	id := tc.mongoContainerID
	handle := reaper.Register(id, func(ctx context.Context) error {
		if _, live := containerCache.Load(id); !live {
			return nil // already killed
		}
		return tc.killMongoContainer(ctx)
	})
	// Locked because the reaper may run the teardown, which reads the
	// handle, the moment Register returns.
	tc.killMu.Lock()
	tc.reaperHandle = handle
	tc.killMu.Unlock()
}

// forgetContainer drops a removed container from the cache and the reaper,
// so it is not killed twice. The caller holds killMu.
func (tc *TestConnection) forgetContainer(id string) {
	containerCache.Delete(id)
	reaper.Unregister(tc.reaperHandle)
}

// ReapRunningContainers kills every container this package started and
// still has cached. A container that cannot be removed stays cached, so a
// later call tries again; the failure goes to the connection's logger.
func ReapRunningContainers() {
	for _, testConn := range getAllCachedConnections() {
		if testConn == nil {
			continue
		}
		fmt.Println("Killing container from ReapRunningContainers")
		_ = testConn.KillMongoContainer()
	}
}
