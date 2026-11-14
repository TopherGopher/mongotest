package mongod_test

import (
	"os"
	"testing"

	"github.com/tophergopher/mongotest/mongod"
)

// The tests assert on images and timeouts that MONGOTEST_* variables also
// set, so a runner that sets one would fail the suite for no fault of the
// code. They are cleared for the whole binary because examples have no
// *testing.T; a test about the environment uses t.Setenv.
func TestMain(m *testing.M) {
	for _, name := range []string{mongod.EnvImage, mongod.EnvStartTimeout, mongod.EnvHostIP} {
		if err := os.Unsetenv(name); err != nil {
			panic("mongod tests: cannot clear " + name + ": " + err.Error())
		}
	}
	os.Exit(m.Run())
}
