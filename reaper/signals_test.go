package reaper

import (
	"os"
	"syscall"
	"testing"

	"github.com/stretchr/testify/assert"
)

// The subprocess test in ignored_test.go proves the rule against a real
// process; this one covers the shapes that are awkward to arrange there,
// including the set that is empty afterwards.
func TestFilterSignalsDropsTheIgnoredOnes(t *testing.T) {
	ignoring := func(ignored ...os.Signal) func(os.Signal) bool {
		return func(sig os.Signal) bool {
			for _, candidate := range ignored {
				if candidate == sig {
					return true
				}
			}
			return false
		}
	}

	cases := []struct {
		name    string
		signals []os.Signal
		ignored func(os.Signal) bool
		want    []os.Signal
		why     string
	}{
		{
			name:    "nothing ignored",
			signals: DefaultSignals(),
			ignored: ignoring(),
			want:    []os.Signal{syscall.SIGINT, syscall.SIGTERM},
			why:     "an ordinary process keeps both, which is the case every other test in this package runs in",
		},
		{
			name:    "a background job under a non-interactive shell",
			signals: DefaultSignals(),
			ignored: ignoring(syscall.SIGINT),
			want:    []os.Signal{syscall.SIGTERM},
			why:     "`go test ./... &` starts with SIGINT ignored; SIGTERM is untouched and is still worth handling, so the package keeps working rather than switching itself off",
		},
		{
			name:    "everything ignored",
			signals: DefaultSignals(),
			ignored: ignoring(syscall.SIGINT, syscall.SIGTERM),
			want:    []os.Signal{},
			why:     "with nothing left to listen for there is no handler to install, and installing one anyway would un-ignore both",
		},
		{
			name:    "an explicit set is filtered the same way",
			signals: []os.Signal{syscall.SIGHUP, syscall.SIGUSR1},
			ignored: ignoring(syscall.SIGHUP),
			want:    []os.Signal{syscall.SIGUSR1},
			why:     "Install takes its own set, and a caller passing SIGHUP is no more entitled to un-ignore it than the default is",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, filterSignals(tc.signals, tc.ignored), tc.why)
		})
	}
}

// A process that ignores everything asked for must not end up with a handler
// it never installed, or Installed would claim one and Register would stop
// trying.
func TestInstallingOnlyIgnoredSignalsInstallsNothing(t *testing.T) {
	t.Cleanup(stopForTest)
	stopForTest()

	registry.mu.Lock()
	installLocked(filterSignals(DefaultSignals(), func(os.Signal) bool { return true }))
	handler := registry.handler
	registry.mu.Unlock()

	assert.Nil(t, handler, "there was nothing to listen for, so no channel may be registered as the handler")
	assert.False(t, Installed(), "and Installed has to say so, both because it is the honest answer and because Register checks it before trying again")
}
