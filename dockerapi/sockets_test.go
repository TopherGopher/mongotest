package dockerapi

import (
	"path/filepath"
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

func TestDefaultHostFindsPodmanMachineOnDarwin(t *testing.T) {
	// podman machine's socket path has moved between releases and carries
	// the machine name, so macOS candidates are globbed rather than fixed.
	// Three shapes are in circulation:
	//
	//   $TMPDIR/podman/<machine>-api.sock                     current
	//   ~/.local/share/containers/podman/machine/qemu/...     podman 4.5+
	//   ~/.local/share/containers/podman/machine/<name>/...   before that
	//
	// The last two differ only in the directory name, so one pattern covers
	// both.
	home, tmp := "/Users/me", "/var/folders/9r/T"
	darwinEnv := env(map[string]string{"HOME": home, "TMPDIR": tmp})
	machineSock := home + "/.local/share/containers/podman/machine/podman-machine-default/podman.sock"
	qemuSock := home + "/.local/share/containers/podman/machine/qemu/podman.sock"
	apiSock := tmp + "/podman/podman-machine-default-api.sock"

	// globbing returns the paths that match, as filepath.Glob would.
	globbing := func(present ...string) func(string) []string {
		return func(pattern string) []string {
			var out []string
			for _, p := range present {
				if ok, _ := filepath.Match(pattern, p); ok {
					out = append(out, p)
				}
			}
			return out
		}
	}

	t.Run("the api socket in TMPDIR", func(t *testing.T) {
		l := lookup{goos: "darwin", getenv: darwinEnv, dialable: sockets(apiSock), glob: globbing(apiSock)}
		got, err := defaultHostForLookup(l)
		require.NoError(t, err, "a podman machine socket must resolve")
		assert.Equal(t, "unix://"+apiSock, got, "the machine's api socket in TMPDIR must be found")
	})

	t.Run("the qemu path used by podman 4.5 and later", func(t *testing.T) {
		l := lookup{goos: "darwin", getenv: darwinEnv, dialable: sockets(qemuSock), glob: globbing(qemuSock)}
		got, err := defaultHostForLookup(l)
		require.NoError(t, err, "the qemu machine socket must resolve")
		assert.Equal(t, "unix://"+qemuSock, got, "one pattern must cover the qemu directory name")
	})

	t.Run("the older path named after the machine", func(t *testing.T) {
		l := lookup{goos: "darwin", getenv: darwinEnv, dialable: sockets(machineSock), glob: globbing(machineSock)}
		got, err := defaultHostForLookup(l)
		require.NoError(t, err, "the older machine socket must resolve")
		assert.Equal(t, "unix://"+machineSock, got, "the same pattern must cover a machine-name directory")
	})

	t.Run("docker.sock still wins, which is what api forwarding uses", func(t *testing.T) {
		// podman machine claims /var/run/docker.sock when API forwarding is
		// on, which is the default. That path is already a candidate, so the
		// common macOS case never reaches the globs.
		l := lookup{goos: "darwin", getenv: darwinEnv, dialable: sockets(rootfulDocker, apiSock), glob: globbing(apiSock)}
		got, err := defaultHostForLookup(l)
		require.NoError(t, err, "the forwarded docker socket must resolve")
		assert.Equal(t, "unix://"+rootfulDocker, got, "api forwarding puts podman on the docker socket, and that is checked first")
	})

	t.Run("a matching path that is not listening is skipped", func(t *testing.T) {
		// A stopped machine leaves its socket behind.
		l := lookup{goos: "darwin", getenv: darwinEnv, dialable: noSockets(), glob: globbing(apiSock, machineSock)}
		got, err := defaultHostForLookup(l)
		require.NoError(t, err, "a stopped machine must not fail discovery")
		assert.Equal(t, defaultUnixSocket, got, "a socket left by a stopped machine must not be used")
	})

	t.Run("the globs do not run on linux", func(t *testing.T) {
		l := lookup{goos: "linux", getenv: darwinEnv, dialable: noSockets(), glob: func(string) []string {
			t.Error("macOS podman machine paths must not be probed on linux")
			return nil
		}}
		_, err := defaultHostForLookup(l)
		require.NoError(t, err, "linux discovery must not touch the machine globs")
	})
}

func TestRootlessFallbackWhenXDGRuntimeDirIsUnset(t *testing.T) {
	// Podman itself falls back to /run/user/$UID when XDG_RUNTIME_DIR is
	// not set, so a client that gives up at that point misses a running
	// rootless daemon. This happens in cron jobs, CI containers and any
	// non-login shell.
	probed := map[string]bool{}
	l := lookup{
		goos:     "linux",
		getenv:   env(nil),
		uid:      1000,
		dialable: func(p string) bool { probed[p] = true; return p == rootlessPodman },
	}
	got, err := defaultHostForLookup(l)
	require.NoError(t, err, "discovery must work without XDG_RUNTIME_DIR")
	assert.Equal(t, "unix://"+rootlessPodman, got, "the /run/user/$UID fallback must find the rootless podman socket")
	assert.True(t, probed[rootlessDocker], "the rootless docker path under /run/user/$UID must be probed too")
}

func TestXDGRuntimeDirWinsOverTheUIDFallback(t *testing.T) {
	// The fallback is a guess; the variable is not.
	l := lookup{
		goos:     "linux",
		getenv:   env(map[string]string{"XDG_RUNTIME_DIR": "/custom/run"}),
		uid:      1000,
		dialable: sockets("/custom/run/podman/podman.sock", rootlessPodman),
	}
	got, err := defaultHostForLookup(l)
	require.NoError(t, err, "discovery with XDG_RUNTIME_DIR set")
	assert.Equal(t, "unix:///custom/run/podman/podman.sock", got, "XDG_RUNTIME_DIR must be used rather than the /run/user/$UID guess")
}

func TestWindowsFindsAPodmanMachineSocket(t *testing.T) {
	// Podman 5.3 and later expose a real AF_UNIX socket on the Windows
	// filesystem, which this client can dial even though it cannot dial a
	// named pipe. Before this, Windows without Docker Desktop's TCP
	// endpoint was simply an error.
	// Paths are built with filepath.Join so the separators match whichever
	// OS runs the test; on Windows itself these are backslashed.
	winTemp := filepath.Join("C:", "Users", "me", "AppData", "Local", "Temp")
	winEnv := env(map[string]string{"TEMP": winTemp})
	apiSock := filepath.Join(winTemp, "podman", "podman-machine-default-api.sock")
	globbing := func(pattern string) []string {
		if ok, _ := filepath.Match(pattern, apiSock); ok {
			return []string{apiSock}
		}
		return nil
	}

	t.Run("the machine socket is used when Docker Desktop is not exposing TCP", func(t *testing.T) {
		l := lookup{goos: "windows", getenv: winEnv, dialable: sockets(apiSock), glob: globbing,
			listening: func(string) bool { return false }}
		got, err := defaultHostForLookup(l)
		require.NoError(t, err, "a podman machine socket must resolve on windows")
		assert.Equal(t, "unix://"+apiSock, got, "the podman machine socket must be used when nothing else answers")
	})

	t.Run("Docker Desktop's TCP endpoint still wins", func(t *testing.T) {
		l := lookup{goos: "windows", getenv: winEnv, dialable: sockets(apiSock), glob: globbing,
			listening: func(addr string) bool { return addr == dockerDesktopTCP }}
		got, err := defaultHostForLookup(l)
		require.NoError(t, err, "the TCP endpoint must resolve")
		assert.Equal(t, "tcp://"+dockerDesktopTCP, got, "docker is preferred over podman on windows too")
	})

	t.Run("with neither, the error mentions both", func(t *testing.T) {
		l := lookup{goos: "windows", getenv: winEnv, dialable: noSockets(), glob: globbing,
			listening: func(string) bool { return false }}
		_, err := defaultHostForLookup(l)
		require.ErrorIs(t, err, ErrConnectionFailed, "nothing reachable on windows is a connection problem")
		assert.Contains(t, err.Error(), "DOCKER_HOST", "the error must name the variable to set")
		assert.Contains(t, err.Error(), "podman", "the error must mention podman now that it is also looked for")
	})
}
