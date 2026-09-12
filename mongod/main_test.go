package mongod_test

import (
	"os"
	"testing"

	"github.com/tophergopher/mongotest/mongod"
)

// This package's own tests and examples assert on exact images, ports and
// budgets, and the code under test reads MONGOTEST_* to decide all three. So a
// runner that sets any of them -- which doc.go actively recommends, to get out
// from under Docker Hub's pull rate limits -- would fail the suite for a reason
// that has nothing to do with the code.
//
// They are cleared for the whole test binary rather than per test, because
// examples have no *testing.T to hang a t.Setenv on and would otherwise be the
// hole in it. A test that is *about* the environment sets what it needs with
// t.Setenv, which restores the empty value afterwards.
func TestMain(m *testing.M) {
	for _, name := range []string{
		mongod.EnvImage,
		mongod.EnvImageRegistry,
		mongod.EnvImageRepository,
		mongod.EnvImageVersion,
		mongod.EnvPort,
		mongod.EnvStartTimeout,
		mongod.EnvHostIP,
	} {
		if err := os.Unsetenv(name); err != nil {
			panic("mongod tests: cannot clear " + name + ": " + err.Error())
		}
	}
	os.Exit(m.Run())
}
