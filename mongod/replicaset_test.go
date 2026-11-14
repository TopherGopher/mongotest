package mongod_test

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/tophergopher/mongotest/dockerclient"
	"github.com/tophergopher/mongotest/dockermock"
	"github.com/tophergopher/mongotest/mongod"
)

// shell makes a mocked daemon record the commands run inside the container,
// and answer each with result.
func shell(m *dockermock.Mock, result dockerclient.ExecResult, err error) (commands *[][]string) {
	commands = new([][]string)
	m.ExecFunc = func(_ context.Context, _ string, cmd ...string) (dockerclient.ExecResult, error) {
		*commands = append(*commands, cmd)
		return result, err
	}
	return commands
}

func TestWithReplicaSetStartsMongodAsAMember(t *testing.T) {
	m, _ := readyMock(t)

	c := mustStart(t, m, mongod.WithReplicaSet("rs0"))

	name, passed := flagValue(created(t, m).Cmd, "--replSet")
	require.True(t, passed, "mongod is started as a replica set member")
	assert.Equal(t, "rs0", name, "of the set that was asked for")
	assert.Equal(t, "rs0", c.ReplicaSet(), "which the container reports")
}

func TestWithReplicaSetInitiatesTheSet(t *testing.T) {
	m, _ := readyMock(t)
	commands := shell(m, dockerclient.ExecResult{}, nil)

	mustStart(t, m, mongod.WithReplicaSet("rs0"))

	assert.Len(t, *commands, 1, "a member that is never initiated accepts no writes, so Start initiates it")
}

func TestAStartWithoutAReplicaSetRunsNothingInTheContainer(t *testing.T) {
	m, _ := readyMock(t)
	commands := shell(m, dockerclient.ExecResult{}, nil)

	mustStart(t, m)

	assert.Empty(t, *commands, "a standalone mongod has nothing to initiate")
}

func TestTheSetIsInitiatedAsTheRootUser(t *testing.T) {
	m, _ := authMock(t)
	commands := shell(m, dockerclient.ExecResult{}, nil)

	mustStart(t, m, mongod.WithReplicaSet("rs0"), mongod.WithAuth("admin", "s3cret"))

	require.Len(t, *commands, 1, "the set is initiated")
	username, _ := flagValue((*commands)[0], "--username")
	password, _ := flagValue((*commands)[0], "--password")
	assert.Equal(t, "admin", username, "a mongod that enforces authentication refuses an anonymous initiate")
	assert.Equal(t, "s3cret", password, "so the command carries the credentials")
}

func TestAStartFailsWhenTheSetCannotBeInitiated(t *testing.T) {
	cases := []struct {
		name   string
		result dockerclient.ExecResult
		err    error
	}{
		{name: "the shell reports a failure", result: dockerclient.ExecResult{ExitCode: 1, Stderr: "MongoServerError: not authorized"}},
		{name: "the image has no shell to run", err: errors.New("exec: mongosh: executable file not found")},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m, _ := readyMock(t)
			shell(m, tc.result, tc.err)

			_, err := start(t, m, mongod.WithReplicaSet("rs0"))

			require.ErrorIs(t, err, mongod.ErrNotInitiated, "a member that accepts no writes is not returned as ready")
			var failed *mongod.StartError
			require.ErrorAs(t, err, &failed, "the error says which step failed")
			assert.Equal(t, mongod.OpInitiate, failed.Op, "and it was the initiation")
			assert.Len(t, m.CallsTo("ContainerRemove"), 1, "the container is removed")
		})
	}
}
