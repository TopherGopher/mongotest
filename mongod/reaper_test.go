package mongod_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/tophergopher/mongotest/dockerclient"
	"github.com/tophergopher/mongotest/mongod"
	"github.com/tophergopher/mongotest/reaper"
)

// A container that outlives the process that started it is the failure these
// tests are about: a test run killed between Start and Stop leaves mongod
// running and a port bound until somebody notices.

func TestStartRegistersTheContainerForReaping(t *testing.T) {
	t.Cleanup(func() { _ = reaper.Reap(context.Background()) })
	m, port := readyMock(t)

	c, err := mongod.Start(context.Background(), mongod.WithDocker(m).WithPort(port))
	require.NoError(t, err, "a start against a fully scripted double must succeed")

	assert.Contains(t, reaper.Names(), c.Name(),
		"a container nobody has stopped yet is registered, so that a signal or an explicit reap can remove it")
}

// Registration has to happen before the readiness wait, not after it. The wait
// runs for up to StartTimeout (a minute by default), and a signal during it
// kills the process before Start's deferred cleanup can run -- so a container
// registered only on success leaks for the whole of that window.
func TestTheContainerIsRegisteredBeforeTheReadinessWait(t *testing.T) {
	t.Cleanup(func() { _ = reaper.Reap(context.Background()) })
	m, port := readyMock(t)
	scriptedTop := m.ContainerTopFunc
	var registeredByThen []string
	m.ContainerTopFunc = func(ctx context.Context, id string) (dockerclient.Top, error) {
		// This runs inside the readiness probe, which is the window in
		// question.
		registeredByThen = reaper.Names()
		return scriptedTop(ctx, id)
	}

	c, err := mongod.Start(context.Background(), mongod.WithDocker(m).WithPort(port))
	require.NoError(t, err, "a start against a fully scripted double must succeed")
	t.Cleanup(func() { _ = c.Stop(context.Background()) })

	assert.Contains(t, registeredByThen, c.Name(),
		"a Ctrl-C during the readiness wait kills the process before the deferred cleanup can run, so the container has to already be registered by then")
}

func TestStopUnregistersTheContainer(t *testing.T) {
	m, port := readyMock(t)
	c, err := mongod.Start(context.Background(), mongod.WithDocker(m).WithPort(port))
	require.NoError(t, err, "a start against a fully scripted double must succeed")

	require.NoError(t, c.Stop(context.Background()), "stopping removes the container")

	assert.NotContains(t, reaper.Names(), c.Name(),
		"a container its owner already removed must not be reaped again: the second removal is a 404 at best, and at worst it is another test's container reusing the name")
}

func TestAFailedStartLeavesNothingRegistered(t *testing.T) {
	m, _ := readyMock(t)
	m.ContainerStartFunc = func(ctx context.Context, id string) error {
		return &dockerclient.StatusError{StatusCode: 500, Method: "POST", Path: "/containers/x/start", Message: "driver failed"}
	}
	before := len(reaper.Names())

	_, err := mongod.Start(context.Background(), mongod.WithDocker(m).WithHostIP("127.0.0.1"))

	require.Error(t, err, "the start failed, which is what this test arranges")
	assert.Len(t, reaper.Names(), before,
		"the failing start already removed the container itself, so registering it would leave the reaper holding a container that no longer exists")
}

func TestReapRunningContainersRemovesWhatIsStillUp(t *testing.T) {
	t.Cleanup(func() { _ = reaper.Reap(context.Background()) })
	f, _ := readyFake(t)
	ports := []int{listenerPort(t), listenerPort(t), listenerPort(t)}
	for _, port := range ports {
		_, err := mongod.Start(context.Background(), mongod.WithDocker(f).WithPort(port))
		require.NoError(t, err, "each start against the fake must succeed")
	}
	require.Len(t, f.Containers(), 3, "three containers are up and none has been stopped")

	require.NoError(t, mongod.ReapRunningContainers(context.Background()),
		"the explicit reap is the one a caller can defer or call from a TestMain, with no signal involved")

	assert.Empty(t, f.Containers(),
		"every container that was still up has been removed, which is the guarantee a signal handler exists to provide and this provides without one")
}

func TestReapRunningContainersIsFineWithNothingToDo(t *testing.T) {
	require.NoError(t, reaper.Reap(context.Background()), "clear whatever an earlier test left")

	assert.NoError(t, mongod.ReapRunningContainers(context.Background()),
		"a process that started no containers has nothing to tear down, so this can be deferred unconditionally")
}

func TestReapReportsAFailedRemoval(t *testing.T) {
	t.Cleanup(func() { _ = reaper.Reap(context.Background()) })
	m, port := readyMock(t)
	removeErr := &dockerclient.StatusError{StatusCode: 500, Method: "DELETE", Path: "/containers/x", Message: "device or resource busy"}
	m.ContainerRemoveFunc = func(ctx context.Context, id string, opts dockerclient.RemoveOptions) error { return removeErr }
	c, err := mongod.Start(context.Background(), mongod.WithDocker(m).WithPort(port))
	require.NoError(t, err, "a start against a fully scripted double must succeed")

	err = mongod.ReapRunningContainers(context.Background())

	require.Error(t, err, "the daemon refused the removal, so the container is still there and saying so is the only useful thing left")
	assert.ErrorIs(t, err, reaper.ErrReap, "callers branch on the sentinel")
	assert.Contains(t, err.Error(), c.Name(), "the message names the container that is still running, because that is what somebody has to go and remove")
}
