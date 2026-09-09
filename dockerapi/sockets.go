package dockerapi

import (
	"net"
	"path/filepath"
	"time"
)

// Well-known socket paths, probed in this order when nothing is configured.
//
// Docker comes before Podman because this is a Docker client, and a machine
// with both running is most likely to mean Docker. Within each, the rootless
// socket comes first, because it belongs to the user running the process
// while the system one may belong to a daemon they cannot talk to.
const (
	// systemDockerSocket is where a system-wide Docker daemon listens.
	systemDockerSocket = "/var/run/docker.sock"
	// systemPodmanSocket is where a system-wide Podman service listens.
	systemPodmanSocket = "/run/podman/podman.sock"
	// rootlessDockerSocketName is joined to XDG_RUNTIME_DIR for rootless Docker.
	rootlessDockerSocketName = "docker.sock"
)

// rootlessPodmanSocketPath is joined to XDG_RUNTIME_DIR for rootless Podman.
var rootlessPodmanSocketPath = []string{"podman", "podman.sock"}

// socketProbeTimeout bounds one connection attempt. The candidates are all
// local, so a listening socket answers immediately and a dead one is refused
// immediately; the timeout only matters for a daemon that is wedged.
const socketProbeTimeout = 100 * time.Millisecond

// socketCandidates lists the unix sockets to probe, in preference order, for
// the platform. Rootless paths are omitted when XDG_RUNTIME_DIR is unset,
// because the path cannot be built without it.
func socketCandidates(goos string, getenv func(string) string) []string {
	runtimeDir := getenv("XDG_RUNTIME_DIR")
	var paths []string
	if runtimeDir != "" {
		paths = append(paths, filepath.Join(runtimeDir, rootlessDockerSocketName))
	}
	paths = append(paths, systemDockerSocket)
	if runtimeDir != "" {
		paths = append(paths, filepath.Join(append([]string{runtimeDir}, rootlessPodmanSocketPath...)...))
	}
	paths = append(paths, systemPodmanSocket)
	return paths
}

// socketDialable reports whether a daemon is listening on the unix socket at
// path. It connects rather than calling os.Stat, because a socket file left
// behind by a stopped daemon still stats successfully and would otherwise
// shadow a daemon that is actually running. That is the common case on a
// machine that used to run Docker and now runs Podman.
func socketDialable(path string) bool {
	conn, err := net.DialTimeout("unix", path, socketProbeTimeout)
	if err != nil {
		return false
	}
	_ = conn.Close()
	return true
}
