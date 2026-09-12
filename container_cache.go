package mongotest

import (
	"sync"
)

var containerCache sync.Map

func cacheConnection(tc *TestConnection) {
	if tc == nil {
		return
	}
	containerCache.Store(tc.mongoContainerID, tc)
	// Registered here rather than from an init(), which is the point: see
	// signal_handler.go. One registration per container rather than one for
	// the whole cache, because reaper.Reap drops every registration, so a
	// single one would stop covering anything cached after the first explicit
	// reap -- and an explicit reap is exactly what the documentation
	// recommends from a TestMain.
	registerForReaping(tc)
}

func getAllCachedConnections() map[string]*TestConnection {
	cachedConnections := map[string]*TestConnection{}
	// Loop over the container cache and unpack into a local map
	containerCache.Range(func(k, v interface{}) bool {
		// key is container ID
		// value is the *TestConnection
		containerID := k.(string)
		testConn := v.(*TestConnection)
		cachedConnections[containerID] = testConn
		return true
	})
	return cachedConnections
}
