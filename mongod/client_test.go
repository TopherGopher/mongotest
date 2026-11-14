package mongod

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// A client holds connections open, and nothing closes one that Start built
// for itself, so starts must not each build their own.
func TestStartsWithoutADockerClientShareOne(t *testing.T) {
	first, err := Resolved{}.client()
	require.NoError(t, err, "a client can be built from the environment")
	second, err := Resolved{}.client()
	require.NoError(t, err, "and asked for again")

	assert.Same(t, first, second, "every start without WithDocker uses the same client")
}
