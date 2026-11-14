package mongod_test

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/tophergopher/mongotest/dockerclient"
	"github.com/tophergopher/mongotest/internal/reaper"
	"github.com/tophergopher/mongotest/mongod"
)

func TestAContainerIsRegisteredForReapingUntilItIsStopped(t *testing.T) {
	m, _ := readyMock(t)
	c := mustStart(t, m)
	assert.Contains(t, reaper.Names(), c.Name(), "a started container is removed if the process is signalled")

	require.NoError(t, c.Stop(context.Background()), "stopping succeeds")
	assert.NotContains(t, reaper.Names(), c.Name(), "and a stopped one is not removed a second time")
}

func TestAContainerIsRegisteredBeforeMongodIsReady(t *testing.T) {
	m, _ := readyMock(t)
	var registeredDuringTheWait []string
	m.ContainerTopFunc = func(context.Context, string) (dockerclient.Top, error) {
		registeredDuringTheWait = reaper.Names()
		return processes("mongod --bind_ip_all"), nil
	}

	c := mustStart(t, m)

	assert.Contains(t, registeredDuringTheWait, c.Name(), "a signal during the readiness wait must not leave the container running")
}

func TestAFailedStartLeavesNothingRegistered(t *testing.T) {
	m, _ := readyMock(t)
	m.ContainerStartFunc = func(context.Context, string) error { return errors.New("no") }
	before := len(reaper.Names())

	_, err := start(t, m)

	require.Error(t, err, "the start fails")
	assert.Len(t, reaper.Names(), before, "and its container, already removed, is not kept for reaping")
}

func TestReapRunningContainersRemovesWhatIsStillUp(t *testing.T) {
	m, _ := readyMock(t)
	c := mustStart(t, m)

	require.NoError(t, mongod.ReapRunningContainers(context.Background()), "the reap succeeds")

	assert.Len(t, m.CallsTo("ContainerRemove"), 1, "the container nobody stopped is removed")
	assert.NotContains(t, reaper.Names(), c.Name(), "and no longer registered")
	assert.NoError(t, mongod.ReapRunningContainers(context.Background()), "a reap with nothing to do is fine")
}

func TestReapRunningContainersReportsWhatItCouldNotRemove(t *testing.T) {
	m, _ := readyMock(t)
	refused := errors.New("the daemon refused")
	m.ContainerRemoveFunc = func(context.Context, string, dockerclient.RemoveOptions) error { return refused }
	mustStart(t, m)

	err := mongod.ReapRunningContainers(context.Background())

	assert.ErrorIs(t, err, mongod.ErrReap, "a container left running is reported")
	assert.ErrorIs(t, err, refused, "with the daemon's own error")
}
