package dockerapi

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// sockets returns a probe that reports the given paths, and only those, as
// having a daemon listening on them.
func sockets(paths ...string) func(string) bool {
	live := make(map[string]bool, len(paths))
	for _, p := range paths {
		live[p] = true
	}
	return func(p string) bool { return live[p] }
}

// noSockets is a probe that finds nothing listening anywhere.
func noSockets() func(string) bool { return func(string) bool { return false } }

const (
	xdg              = "/run/user/1000"
	rootlessDocker   = "/run/user/1000/docker.sock"
	rootfulDocker    = "/var/run/docker.sock"
	rootlessPodman   = "/run/user/1000/podman/podman.sock"
	rootfulPodmanRun = "/run/podman/podman.sock"
)

func TestDefaultHostProbesForASocket(t *testing.T) {
	// Nothing is configured, so discovery falls back to looking for a
	// daemon. Docker is preferred over Podman because this is a Docker
	// client and that is the least surprising answer when both are present,
	// and a rootless socket is preferred over the system one because a
	// rootless daemon is the one belonging to the user running the tests.
	withXDG := env(map[string]string{"XDG_RUNTIME_DIR": xdg})

	t.Run("rootless docker wins when everything is present", func(t *testing.T) {
		got, err := defaultHostFor("linux", withXDG,
			sockets(rootlessDocker, rootfulDocker, rootlessPodman, rootfulPodmanRun), nil)
		require.NoError(t, err, "a listening socket must resolve without error")
		assert.Equal(t, "unix://"+rootlessDocker, got, "the user's own docker socket is preferred over every other candidate")
	})

	t.Run("system docker when there is no rootless docker", func(t *testing.T) {
		got, err := defaultHostFor("linux", withXDG, sockets(rootfulDocker, rootlessPodman), nil)
		require.NoError(t, err, "the system docker socket must resolve")
		assert.Equal(t, "unix://"+rootfulDocker, got, "docker is preferred over podman when both are listening")
	})

	t.Run("rootless podman when there is no docker at all", func(t *testing.T) {
		got, err := defaultHostFor("linux", withXDG, sockets(rootlessPodman, rootfulPodmanRun), nil)
		require.NoError(t, err, "a podman socket must resolve")
		assert.Equal(t, "unix://"+rootlessPodman, got, "rootless podman is preferred over the system podman socket")
	})

	t.Run("system podman is the last candidate", func(t *testing.T) {
		got, err := defaultHostFor("linux", withXDG, sockets(rootfulPodmanRun), nil)
		require.NoError(t, err, "the system podman socket must resolve")
		assert.Equal(t, "unix://"+rootfulPodmanRun, got, "the system podman socket is used when it is the only one listening")
	})

	t.Run("XDG_RUNTIME_DIR unset skips the rootless candidates", func(t *testing.T) {
		// The rootless paths are built from XDG_RUNTIME_DIR. Without it
		// there is no path to build, and probing a relative one would be
		// worse than not probing at all.
		probed := map[string]bool{}
		probe := func(p string) bool { probed[p] = true; return p == rootfulDocker }
		got, err := defaultHostFor("linux", env(nil), probe, nil)
		require.NoError(t, err, "discovery must still work with no XDG_RUNTIME_DIR")
		assert.Equal(t, "unix://"+rootfulDocker, got, "the system docker socket is found without XDG_RUNTIME_DIR")
		for p := range probed {
			assert.NotContains(t, p, "/run/user/", "no rootless path may be probed when XDG_RUNTIME_DIR is unset: %q", p)
		}
	})

	t.Run("a stale socket file is skipped for a live one", func(t *testing.T) {
		// The probe connects rather than calling Stat, so a socket left
		// behind by a stopped daemon does not shadow a running one. This is
		// the case that makes the whole feature worth having on a machine
		// that used to run docker and now runs podman.
		got, err := defaultHostFor("linux", withXDG, sockets(rootlessPodman), nil)
		require.NoError(t, err, "a live podman socket must be found")
		assert.Equal(t, "unix://"+rootlessPodman, got, "a dead docker socket must not win over a live podman one")
	})

	t.Run("nothing listening falls back to the docker default", func(t *testing.T) {
		// Deliberately not an error here. The connection error raised at
		// dial time already names the socket and says what to do, and
		// failing during construction would move the failure for callers
		// who build a client eagerly.
		got, err := defaultHostFor("linux", withXDG, noSockets(), nil)
		require.NoError(t, err, "discovery must not fail when nothing is listening")
		assert.Equal(t, defaultUnixSocket, got, "the docker default is the fallback so the dial produces the actionable error")
	})
}

func TestResolveHostPrefersConfigurationOverProbing(t *testing.T) {
	// The probe is a fallback, never an override. An explicit DOCKER_HOST or
	// a selected context is the user telling us where the daemon is, and a
	// socket happening to exist locally must not overrule that.
	live := sockets(rootlessPodman, rootfulDocker)

	t.Run("DOCKER_HOST wins over a listening socket", func(t *testing.T) {
		l := lookup{goos: "linux", getenv: env(map[string]string{
			"DOCKER_HOST":     "tcp://1.2.3.4:2375",
			"XDG_RUNTIME_DIR": xdg,
		}), dialable: func(string) bool {
			t.Error("no socket may be probed when DOCKER_HOST is set")
			return true
		}}
		got, err := resolveHostWith(l, t.TempDir())
		require.NoError(t, err, "resolution with DOCKER_HOST set")
		assert.Equal(t, "tcp://1.2.3.4:2375", got, "DOCKER_HOST must take precedence over any local socket")
	})

	t.Run("a docker context wins over a listening socket", func(t *testing.T) {
		cfg := t.TempDir()
		writeContext(t, cfg, "remote", "tcp://10.1.2.3:2376")
		l := lookup{goos: "linux", getenv: env(map[string]string{
			"DOCKER_CONTEXT":  "remote",
			"XDG_RUNTIME_DIR": xdg,
		}), dialable: func(string) bool {
			t.Error("no socket may be probed when a context is selected")
			return true
		}}
		got, err := resolveHostWith(l, cfg)
		require.NoError(t, err, "resolution through a context")
		assert.Equal(t, "tcp://10.1.2.3:2376", got, "a selected context must take precedence over any local socket")
	})

	t.Run("probing happens only when nothing is configured", func(t *testing.T) {
		l := lookup{goos: "linux", getenv: env(map[string]string{"XDG_RUNTIME_DIR": xdg}), dialable: live}
		got, err := resolveHostWith(l, t.TempDir())
		require.NoError(t, err, "resolution with nothing configured")
		assert.Equal(t, "unix://"+rootfulDocker, got, "with nothing configured the probe decides")
	})
}
