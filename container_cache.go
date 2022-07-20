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
