package mongod_test

import (
	"net"
	"strconv"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/tophergopher/mongotest/mongod"
)

func TestGetAvailablePortReturnsABindablePort(t *testing.T) {
	port, err := mongod.GetAvailablePort()

	require.NoError(t, err, "a machine with a free port must be able to report one")
	assert.Greater(t, port, 0, "a port number is positive, and zero is the request to be assigned one rather than an answer")
	assert.LessOrEqual(t, port, 65535, "65535 is the highest port there is")

	ln, err := net.Listen("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(port)))
	require.NoError(t, err, "the point of the call is that the port can then be bound; the listener it used to find the port has to have been closed again")
	require.NoError(t, ln.Close(), "closing the test's own listener must succeed")
}

func TestGetAvailablePortDoesNotRepeatItself(t *testing.T) {
	// Each call asks the kernel for a fresh port while the previous ones are
	// still held open, which is the closest this can get to the parallel case.
	const ports = 8
	var held []net.Listener
	seen := map[int]bool{}
	for i := range ports {
		port, err := mongod.GetAvailablePort()
		require.NoError(t, err, "call %d must find a free port", i)
		require.False(t, seen[port], "port %d was handed out twice, and two containers pinned to it would fight over the bind", port)
		seen[port] = true

		ln, err := net.Listen("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(port)))
		require.NoError(t, err, "port %d was reported free, so binding it must succeed", port)
		held = append(held, ln)
	}
	for _, ln := range held {
		require.NoError(t, ln.Close(), "closing the test's listeners must succeed")
	}
}

func TestDetectContainerisationExplainsItself(t *testing.T) {
	got := mongod.DetectContainerisation()

	assert.NotEmpty(t, got.Signal, "the answer decides where every container is dialled, so it has to say what it was based on for a wrong one to be diagnosable")
}
