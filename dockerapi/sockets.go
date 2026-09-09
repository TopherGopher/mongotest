package dockerapi

import (
	"net"
	"path/filepath"
	"strconv"
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

// machineSocketGlobs are the macOS podman machine socket locations. They are
// globbed rather than listed because the path carries the machine name, and
// because it has moved between releases:
//
//	$TMPDIR/podman/<machine>-api.sock                        current
//	~/.local/share/containers/podman/machine/qemu/...        podman 4.5 and later
//	~/.local/share/containers/podman/machine/<machine>/...   before that
//
// The last two differ only in a directory name, so one pattern covers both.
//
// This is a fallback. podman machine forwards the API to
// /var/run/docker.sock by default, and that candidate is checked first.
func machineSocketGlobs(getenv func(string) string) []string {
	var patterns []string
	if tmp := getenv("TMPDIR"); tmp != "" {
		patterns = append(patterns, filepath.Join(tmp, "podman", "*-api.sock"))
	}
	if home := getenv("HOME"); home != "" {
		patterns = append(patterns, filepath.Join(home, ".local", "share", "containers", "podman", "machine", "*", "podman.sock"))
	}
	return patterns
}

// globPaths returns the paths matching pattern, discarding the error, since a
// malformed pattern is a bug here rather than a condition a caller can act on
// and an unreadable directory simply means no candidate.
func globPaths(pattern string) []string {
	matches, err := filepath.Glob(pattern)
	if err != nil {
		return nil
	}
	return matches
}

// socketProbeTimeout bounds one connection attempt. The candidates are all
// local, so a listening socket answers immediately and a dead one is refused
// immediately; the timeout only matters for a daemon that is wedged.
const socketProbeTimeout = 100 * time.Millisecond

// socketCandidates lists the unix sockets to probe, in preference order.
// Rootless paths are omitted when XDG_RUNTIME_DIR is unset, because the path
// cannot be built without it.
func socketCandidates(l lookup) []string {
	if l.goos == "windows" {
		return windowsMachineSockets(l)
	}
	runtimeDir := runtimeDirFor(l)
	var paths []string
	if runtimeDir != "" {
		paths = append(paths, filepath.Join(runtimeDir, rootlessDockerSocketName))
	}
	paths = append(paths, systemDockerSocket)
	if runtimeDir != "" {
		paths = append(paths, filepath.Join(append([]string{runtimeDir}, rootlessPodmanSocketPath...)...))
	}
	paths = append(paths, systemPodmanSocket)
	if l.goos == "darwin" && l.glob != nil {
		for _, pattern := range machineSocketGlobs(l.getenv) {
			paths = append(paths, l.glob(pattern)...)
		}
	}
	return paths
}

// runtimeDirFor returns the directory rootless sockets live under.
// XDG_RUNTIME_DIR when it is set, and otherwise /run/user/<uid>, which is
// the same fallback Podman itself applies when starting its service. Without
// it a rootless daemon is invisible to any process with no XDG_RUNTIME_DIR,
// which is the normal state of a cron job or a CI container.
func runtimeDirFor(l lookup) string {
	if dir := l.getenv("XDG_RUNTIME_DIR"); dir != "" {
		return dir
	}
	if l.uid > 0 {
		return "/run/user/" + strconv.Itoa(l.uid)
	}
	return ""
}

// windowsMachineSockets are the AF_UNIX sockets podman machine exposes on
// the Windows filesystem, which this client can dial even though it cannot
// dial the named pipe alongside them. Added in Podman 5.3.
func windowsMachineSockets(l lookup) []string {
	if l.glob == nil {
		return nil
	}
	var dirs []string
	if tmp := l.getenv("TEMP"); tmp != "" {
		dirs = append(dirs, tmp)
	}
	if local := l.getenv("LOCALAPPDATA"); local != "" {
		dirs = append(dirs, filepath.Join(local, "Temp"))
	}
	var paths []string
	for _, dir := range dirs {
		paths = append(paths, l.glob(filepath.Join(dir, "podman", "*-api.sock"))...)
	}
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
