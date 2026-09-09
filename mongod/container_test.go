package mongod_test

import (
	"context"
	"errors"
	"fmt"
	"net"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/tophergopher/mongotest/dockerclient"
	"github.com/tophergopher/mongotest/dockermock"
	"github.com/tophergopher/mongotest/mongod"
)

func TestStartReportsWhatItCreated(t *testing.T) {
	m, port := readyMock(t)

	c, err := mongod.Start(context.Background(), mongod.WithDocker(m), mongod.WithPort(port))
	require.NoError(t, err, "a start against a fully scripted double must succeed")
	t.Cleanup(func() { _ = c.Stop(context.Background()) })

	assert.Equal(t, "3f1a9c4b7e2d5a8f0b6c3e9d1a4f7b2c5e8d0a3f6b9c2e5d8a1f4b7c0e3d6a9f", c.ID(), "ID reports the container id the daemon returned, which is what every later call is addressed to")
	assert.Equal(t, port, c.Port(), "Port reports the host port read back from inspect, which is the one a client connects to")
	assert.Equal(t, "127.0.0.1", c.Host(), "the daemon is local and this process is not containerised, so the published port is reachable on loopback")
	assert.Equal(t, "mongodb://127.0.0.1:"+strconv.Itoa(port)+"/?directConnection=true",
		c.URI(), "directConnection stops the driver from trying to discover a topology that a single container does not have")
}

func TestStartPullsOnceWhenTheImageIsMissing(t *testing.T) {
	m, port := readyMock(t)
	create := m.ContainerCreateFunc
	var creates int
	m.ContainerCreateFunc = func(ctx context.Context, name string, cfg dockerclient.ContainerConfig) (string, []string, error) {
		creates++
		if creates == 1 {
			return "", nil, &dockerclient.StatusError{StatusCode: 404, Method: "POST", Path: "/containers/create", Message: "No such image: mongo:8"}
		}
		return create(ctx, name, cfg)
	}

	c, err := mongod.Start(context.Background(), mongod.WithDocker(m), mongod.WithPort(port))
	require.NoError(t, err, "a create that fails only because the image is missing is recoverable by pulling it")
	t.Cleanup(func() { _ = c.Stop(context.Background()) })

	assert.Equal(t,
		[]string{"Host", "ContainerCreate", "ImagePull", "ContainerCreate", "ContainerStart", "ContainerInspect", "ContainerTop"},
		m.Methods(), "creating first and pulling only on a miss keeps the common case to one call; the address is resolved before anything is created so an unreachable one costs nothing")
	pulls := m.CallsTo("ImagePull")
	require.Len(t, pulls, 1, "the pull is a retry, not a loop: a second miss is a real error")
	assert.Equal(t, []any{"mongo:8"}, pulls[0].Args, "the pull must name the image the create asked for")
}

func TestStartReturnsAPullFailureUntouched(t *testing.T) {
	m, port := readyMock(t)
	pullErr := &dockerclient.PullError{Ref: "mongo:8", Message: "manifest unknown"}
	m.ContainerCreateFunc = func(ctx context.Context, name string, cfg dockerclient.ContainerConfig) (string, []string, error) {
		return "", nil, &dockerclient.StatusError{StatusCode: 404, Method: "POST", Path: "/containers/create", Message: "No such image: mongo:8"}
	}
	m.ImagePullFunc = func(ctx context.Context, ref string) error { return pullErr }

	_, err := mongod.Start(context.Background(), mongod.WithDocker(m), mongod.WithPort(port))

	require.Error(t, err, "there is no container without an image, so a failed pull fails the start")
	assert.ErrorIs(t, err, pullErr, "the registry's own explanation is the actionable part, so it is wrapped rather than replaced")
	assert.Empty(t, m.CallsTo("ContainerRemove"), "nothing was created, so there is nothing to remove")
}

func TestStartRemovesTheContainerWhenStartFails(t *testing.T) {
	m, port := readyMock(t)
	startErr := &dockerclient.StatusError{StatusCode: 500, Method: "POST", Path: "/containers/x/start", Message: "driver failed programming external connectivity"}
	m.ContainerStartFunc = func(ctx context.Context, id string) error { return startErr }

	_, err := mongod.Start(context.Background(), mongod.WithDocker(m), mongod.WithPort(port))

	require.Error(t, err, "a container that will not start is not a usable result")
	assert.ErrorIs(t, err, startErr, "the daemon's reason for refusing is the actionable part of the error")
	removes := m.CallsTo("ContainerRemove")
	require.Len(t, removes, 1, "a created container that never became usable is the caller's mess to inherit unless the failing start cleans it up")
	assert.Equal(t, dockerclient.RemoveOptions{Force: true, RemoveVolumes: true},
		removes[0].Args[1], "the container may already be running and may have anonymous volumes, so the cleanup has to force both")
}

func TestStartFailsAndCleansUpWhenNoPortIsPublished(t *testing.T) {
	m, _ := readyMock(t)
	m.ContainerInspectFunc = func(ctx context.Context, id string) (dockerclient.ContainerInspect, error) {
		return dockerclient.ContainerInspect{ID: id, State: dockerclient.ContainerState{Status: "running", Running: true}}, nil
	}

	_, err := mongod.Start(context.Background(), mongod.WithDocker(m))

	require.Error(t, err, "a container whose port was not published cannot be connected to, so reporting success would hand back something unusable")
	assert.ErrorIs(t, err, mongod.ErrNoPublishedPort, "callers branch on the sentinel rather than on the message")
	assert.Contains(t, err.Error(), "27017/tcp", "the message has to name the port that is missing for the reader to act on it")
	assert.Len(t, m.CallsTo("ContainerRemove"), 1, "the container exists at this point, so the failing start removes it")
}

func TestStartTimesOutWhenNothingListens(t *testing.T) {
	m, _ := readyMock(t)
	dead, err := mongod.GetAvailablePort()
	require.NoError(t, err, "the test needs a free port, so that the readiness probe finds nothing listening on it")
	m.ContainerInspectFunc = func(ctx context.Context, id string) (dockerclient.ContainerInspect, error) {
		return inspectPublishing(id, dead), nil
	}

	_, err = mongod.Start(context.Background(), mongod.WithDocker(m), mongod.WithStartTimeout(200*time.Millisecond))

	require.Error(t, err, "a container nothing can connect to is not ready, however healthy the daemon says it is")
	assert.ErrorIs(t, err, context.DeadlineExceeded, "the wait ended because it ran out of time, and a caller distinguishes that from a refusal with errors.Is")
	assert.ErrorIs(t, err, mongod.ErrNotReady, "the sentinel is what a caller branches on to decide whether to retry with a longer timeout")
	assert.Contains(t, err.Error(), strconv.Itoa(dead), "the message names the port that was dialled, because that is what the reader has to go and look at")
	assert.Len(t, m.CallsTo("ContainerRemove"), 1, "a container that never became ready is removed rather than left running")
}

func TestReadinessWaitsForMongodNotJustForTheOpenPort(t *testing.T) {
	m, port := readyMock(t)
	// Docker's userland proxy binds the published port as soon as the
	// container is created, so the socket accepts connections while the only
	// process inside is runc init. Report that state for the first few polls.
	var tops int
	m.ContainerTopFunc = func(ctx context.Context, id string) (dockerclient.Top, error) {
		tops++
		if tops < 3 {
			return dockerclient.Top{Titles: []string{"PID", "COMMAND"}, Processes: [][]string{{"1", "/usr/bin/runc init"}}}, nil
		}
		return mongodProcesses(), nil
	}

	c, err := mongod.Start(context.Background(), mongod.WithDocker(m), mongod.WithPort(port), mongod.WithStartTimeout(10*time.Second))
	require.NoError(t, err, "mongod appears in the process listing before the timeout, so the start succeeds")
	t.Cleanup(func() { _ = c.Stop(context.Background()) })

	assert.GreaterOrEqual(t, tops, 3, "a listening socket is not proof that mongod is up, so the probe keeps polling the process listing until mongod is in it")
}

func TestReadinessSurvivesATopThatIsNotReadyYet(t *testing.T) {
	m, port := readyMock(t)
	var tops int
	m.ContainerTopFunc = func(ctx context.Context, id string) (dockerclient.Top, error) {
		tops++
		if tops == 1 {
			// The daemon answers 409 for a container that has not started yet.
			return dockerclient.Top{}, &dockerclient.StatusError{StatusCode: 409, Method: "GET", Path: "/containers/x/top", Message: "Container x is not running"}
		}
		return mongodProcesses(), nil
	}

	c, err := mongod.Start(context.Background(), mongod.WithDocker(m), mongod.WithPort(port), mongod.WithStartTimeout(10*time.Second))

	require.NoError(t, err, "a container that is not running yet is the normal state during a start, not a failure to report")
	t.Cleanup(func() { _ = c.Stop(context.Background()) })
}

func TestStopRemovesTheContainerAndIsIdempotent(t *testing.T) {
	m, port := readyMock(t)
	c, err := mongod.Start(context.Background(), mongod.WithDocker(m), mongod.WithPort(port))
	require.NoError(t, err, "a start against a fully scripted double must succeed")

	require.NoError(t, c.Stop(context.Background()), "removing a container that exists must succeed")
	require.NoError(t, c.Stop(context.Background()), "Stop is called from defers and from cleanup handlers, so calling it twice must not be an error")

	removes := m.CallsTo("ContainerRemove")
	require.Len(t, removes, 1, "the second Stop has nothing left to do, so it must not ask the daemon again")
	assert.Equal(t, c.ID(), removes[0].Args[0], "the removal has to name this container and no other")
	assert.Equal(t, dockerclient.RemoveOptions{Force: true, RemoveVolumes: true},
		removes[0].Args[1], "mongod is still running and the image declares an anonymous volume for /data/db, so both flags are needed for the removal to succeed and to leave nothing behind")
}

func TestStopTreatsAnAlreadyRemovedContainerAsSuccess(t *testing.T) {
	m, port := readyMock(t)
	m.ContainerRemoveFunc = func(ctx context.Context, id string, opts dockerclient.RemoveOptions) error {
		return &dockerclient.StatusError{StatusCode: 404, Method: "DELETE", Path: "/containers/" + id, Message: "No such container: " + id}
	}
	c, err := mongod.Start(context.Background(), mongod.WithDocker(m), mongod.WithPort(port))
	require.NoError(t, err, "a start against a fully scripted double must succeed")

	assert.NoError(t, c.Stop(context.Background()), "a container someone else already removed is the state Stop is trying to reach, so it is success and not a failure")
}

func TestStopReturnsTheFailureAndAllowsARetry(t *testing.T) {
	m, port := readyMock(t)
	removeErr := &dockerclient.StatusError{StatusCode: 500, Method: "DELETE", Path: "/containers/x", Message: "device or resource busy"}
	var fail = true
	m.ContainerRemoveFunc = func(ctx context.Context, id string, opts dockerclient.RemoveOptions) error {
		if fail {
			return removeErr
		}
		return nil
	}
	c, err := mongod.Start(context.Background(), mongod.WithDocker(m), mongod.WithPort(port))
	require.NoError(t, err, "a start against a fully scripted double must succeed")

	require.ErrorIs(t, c.Stop(context.Background()), removeErr, "a removal the daemon refused is reported, because the container is still there")
	fail = false
	assert.NoError(t, c.Stop(context.Background()), "the first failure did not remove the container, so a retry has to be allowed to try again rather than being swallowed as already stopped")
	assert.Len(t, m.CallsTo("ContainerRemove"), 2, "the retry reaches the daemon; only a removal that succeeded ends the container's life")
}

func TestStopOnANilContainerIsNil(t *testing.T) {
	var c *mongod.Container

	assert.NoError(t, c.Stop(context.Background()), "Start returns a nil container alongside an error, and the usual defer runs Stop on it without checking, so a nil receiver must be safe")
}

func TestEndpointReportsTheResolvedAddress(t *testing.T) {
	m, port := readyMock(t)
	c, err := mongod.Start(context.Background(), mongod.WithDocker(m), mongod.WithPort(port))
	require.NoError(t, err, "a start against a fully scripted double must succeed")
	t.Cleanup(func() { _ = c.Stop(context.Background()) })

	host, got, err := c.Endpoint("27017/tcp")

	require.NoError(t, err, "27017/tcp is published, so it has an address")
	assert.Equal(t, "127.0.0.1", host, "the daemon is local and this process is not containerised, so the published port is on loopback")
	assert.Equal(t, port, got, "Endpoint reports the host port the daemon published, which is the one to dial")
}

func TestEndpointRejectsAPortThatIsNotPublished(t *testing.T) {
	m, port := readyMock(t)
	c, err := mongod.Start(context.Background(), mongod.WithDocker(m), mongod.WithPort(port))
	require.NoError(t, err, "a start against a fully scripted double must succeed")
	t.Cleanup(func() { _ = c.Stop(context.Background()) })

	_, _, err = c.Endpoint("28017/tcp")

	require.Error(t, err, "a port that was never published has no address, and answering with one would send the caller somewhere wrong")
	assert.ErrorIs(t, err, mongod.ErrNoPublishedPort, "callers branch on the sentinel rather than on the message")
	assert.Contains(t, err.Error(), "28017/tcp", "the message names the port that was asked for")
}

func TestWithHostIPOverridesDetection(t *testing.T) {
	m, port := readyMock(t)
	m.HostFunc = func() string { return "unix:///var/run/docker.sock" }

	c, err := mongod.Start(context.Background(), mongod.WithDocker(m), mongod.WithPort(port), mongod.WithHostIP("127.0.0.1"))
	require.NoError(t, err, "a start against a fully scripted double must succeed")
	t.Cleanup(func() { _ = c.Stop(context.Background()) })

	assert.Equal(t, "127.0.0.1", c.Host(), "WithHostIP is the escape hatch for a topology no detection covers, so it is honoured without any detection running at all")
}

func TestMongotestHostIPEnvIsHonoured(t *testing.T) {
	t.Setenv("MONGOTEST_HOST_IP", "127.0.0.1")
	m, port := readyMock(t)
	m.HostFunc = func() string { return "unix:///var/run/docker.sock" }

	c, err := mongod.Start(context.Background(), mongod.WithDocker(m), mongod.WithPort(port))
	require.NoError(t, err, "a start against a fully scripted double must succeed")
	t.Cleanup(func() { _ = c.Stop(context.Background()) })

	assert.Equal(t, "127.0.0.1", c.Host(), "MONGOTEST_HOST_IP is how a CI job that cannot pass options configures the address")
}

func TestTCPDaemonHostIsWhereThePortIsPublished(t *testing.T) {
	m, port := readyMock(t)
	m.HostFunc = func() string { return "tcp://127.0.0.1:2375" }

	c, err := mongod.Start(context.Background(), mongod.WithDocker(m), mongod.WithPort(port))
	require.NoError(t, err, "a start against a fully scripted double must succeed")
	t.Cleanup(func() { _ = c.Stop(context.Background()) })

	assert.Equal(t, "127.0.0.1", c.Host(), "a container's published port lands on the machine running the daemon, so a tcp:// daemon host is where to dial")
}

func TestParallelStartsAgainstOneClientEachRemoveTheirOwn(t *testing.T) {
	const containers = 12
	f, _ := readyFake(t)
	ports := make([]int, containers)
	for i := range ports {
		ports[i] = listenerPort(t)
	}

	t.Run("subtests", func(t *testing.T) {
		for i := range containers {
			t.Run(fmt.Sprintf("container-%02d", i), func(t *testing.T) {
				t.Parallel()
				c, err := mongod.Start(context.Background(), mongod.WithDocker(f), mongod.WithPort(ports[i]))
				require.NoError(t, err, "starts from parallel subtests share one client, which must be safe for concurrent use")
				assert.Equal(t, ports[i], c.Port(), "each container reports its own published port and not another subtest's")
				require.NoError(t, c.Stop(context.Background()), "each subtest removes the container it started")
			})
		}
	})

	assert.Empty(t, f.Containers(), "every start is paired with a stop, so nothing is left behind for the next test to trip over")

	var wg sync.WaitGroup
	ids := make([]string, containers)
	errs := make([]error, containers)
	for i := range containers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			c, err := mongod.Start(context.Background(), mongod.WithDocker(f), mongod.WithPort(ports[i]))
			if err != nil {
				errs[i] = err
				return
			}
			ids[i] = c.ID()
			errs[i] = c.Stop(context.Background())
		}()
	}
	wg.Wait()

	for i, err := range errs {
		require.NoError(t, err, "goroutine %d shares the client with eleven others, and concurrent use must not corrupt what any of them sees", i)
	}
	seen := map[string]bool{}
	for i, id := range ids {
		require.NotEmpty(t, id, "goroutine %d must have got a container id back", i)
		require.False(t, seen[id], "two goroutines came back with container id %s, which means one of them was handed another's container", id)
		seen[id] = true
	}
	assert.Empty(t, f.Containers(), "each goroutine removed exactly its own container, so the store ends empty")
}

func TestStartAgainstAFakeRunsTheWholeLifecycle(t *testing.T) {
	f, port := readyFake(t)

	c, err := mongod.Start(context.Background(), mongod.WithDocker(f), mongod.WithPort(port), mongod.WithReplicaSet("rs0"))
	require.NoError(t, err, "the Fake enforces the daemon's real rules, so a start that passes against it is one the daemon would accept")

	require.Len(t, f.Containers(), 1, "one start creates exactly one container")
	state := f.Containers()[0]
	assert.Equal(t, "mongo:8", state.Config.Image, "the Fake records what was actually created")
	assert.Equal(t, []string{"--replSet", "rs0"}, state.Config.Cmd, "the replica set flag reaches the daemon")
	assert.True(t, state.Running, "the container is started, not merely created")

	conn, err := net.DialTimeout("tcp", net.JoinHostPort(c.Host(), strconv.Itoa(c.Port())), time.Second)
	require.NoError(t, err, "the address the container reports is the one a caller dials, so it has to be connectable")
	require.NoError(t, conn.Close(), "closing the probe connection must succeed")

	require.NoError(t, c.Stop(context.Background()), "stopping removes the container")
	assert.Empty(t, f.Containers(), "a stopped container is removed, not merely killed, so the daemon is left as it was found")
}

func TestStartWithoutAnImageInTheFakePullsIt(t *testing.T) {
	f := dockermock.NewFake()
	f.Processes = func(c dockermock.ContainerState) [][]string { return mongodProcesses().Processes }
	port := listenerPort(t)

	c, err := mongod.Start(context.Background(), mongod.WithDocker(f), mongod.WithPort(port))

	require.NoError(t, err, "an image that is not present locally is pulled and the create retried, which is the first-run experience on a clean machine")
	t.Cleanup(func() { _ = c.Stop(context.Background()) })
	inspect, err := f.ImageInspect(context.Background(), "mongo:8")
	require.NoError(t, err, "the pull records the image, so inspecting it afterwards must succeed")
	assert.Contains(t, inspect.RepoTags, "mongo:8", "the image that was pulled is the one the create asked for")
}

func TestStartReturnsTheCreateFailureUntouched(t *testing.T) {
	m := &dockermock.Mock{}
	createErr := &dockerclient.StatusError{StatusCode: 409, Method: "POST", Path: "/containers/create", Message: "Conflict. The container name is already in use"}
	m.ContainerCreateFunc = func(ctx context.Context, name string, cfg dockerclient.ContainerConfig) (string, []string, error) {
		return "", nil, createErr
	}

	_, err := mongod.Start(context.Background(), mongod.WithDocker(m), mongod.WithName("mongotest-taken"))

	require.Error(t, err, "a create the daemon refused is a failed start")
	assert.ErrorIs(t, err, dockerclient.ErrConflict, "a name clash is a conflict, and a caller retrying with a different name branches on that")
	assert.True(t, errors.Is(err, createErr), "the daemon's own explanation is what tells the reader which name is taken, so it is wrapped rather than replaced")
	assert.Empty(t, m.CallsTo("ImagePull"), "the create failed for a reason a pull cannot fix, so pulling would only waste time before failing again")
}
