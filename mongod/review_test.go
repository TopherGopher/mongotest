package mongod_test

import (
	"context"
	"errors"
	"net"
	"strconv"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/tophergopher/mongotest/dockerclient"
	"github.com/tophergopher/mongotest/dockermock"
	"github.com/tophergopher/mongotest/mongod"
)

// The cases in this file come from the review of the pull request that added
// this package. Each one is a bug that was found and fixed; the test is what
// keeps it fixed.

// unreachableHost is in TEST-NET-1, which is reserved for documentation and
// is never routable, so a readiness probe aimed at it always fails. That
// makes a test about the create request deterministic without needing a
// second network namespace to run in.
const unreachableHost = "192.0.2.1"

// A published port is only reachable at the address it was bound to. Docker
// takes HostIP literally: docker-proxy listens on that address alone and the
// DNAT rule matches that address alone. Resolving the gateway and then
// binding loopback therefore produced a container that nothing outside the
// daemon's own namespace could reach, which is the whole failure this
// resolution exists to prevent.
func TestCreateRequestBindsAllInterfacesWhenTheAddressIsNotLoopback(t *testing.T) {
	m, _ := readyMock(t)

	_, err := mongod.Start(context.Background(),
		mongod.WithDocker(m),
		mongod.WithHostIP(unreachableHost),
		mongod.WithStartTimeout(150*time.Millisecond),
	)
	require.Error(t, err, "nothing answers in TEST-NET-1, so this start cannot succeed; the create request is what the test is about")

	cfg := createdConfig(t, m)
	require.NotNil(t, cfg.HostConfig, "the port bindings live on HostConfig, so it must be sent")
	require.Len(t, cfg.HostConfig.PortBindings["27017/tcp"], 1, "27017 is published exactly once")
	assert.Equal(t, "0.0.0.0", cfg.HostConfig.PortBindings["27017/tcp"][0].HostIP,
		"the caller will dial an address that is not this machine's loopback, and a port bound to 127.0.0.1 is refused from anywhere else, so the bind has to cover the interface that address arrives on")
}

func TestCreateRequestKeepsLoopbackWhenThatIsWhereItWillBeDialled(t *testing.T) {
	m, port := readyMock(t)

	c, err := mongod.Start(context.Background(), mongod.WithDocker(m), mongod.WithPort(port))
	require.NoError(t, err, "a start against a fully scripted double must succeed")
	t.Cleanup(func() { _ = c.Stop(context.Background()) })

	cfg := createdConfig(t, m)
	assert.Equal(t, "127.0.0.1", cfg.HostConfig.PortBindings["27017/tcp"][0].HostIP,
		"the laptop case dials loopback, so the container stays off the machine's other interfaces; widening the bind there would expose an unauthenticated database to the network for no benefit")
}

// The readiness gate looked for "mongod" in every column of the process
// listing. The mongo image's entrypoint is a shell script that takes mongod
// as its argument, and the user it drops to is called mongodb, so the gate
// matched before mongod existed at all.
func TestReadinessIsNotSatisfiedByTheEntrypointOrTheUserName(t *testing.T) {
	// Captured from `docker top` against mongo:8 during a real start.
	startupListings := []dockerclient.Top{
		{Titles: []string{"UID", "PID", "PPID", "C", "STIME", "TTY", "TIME", "CMD"},
			Processes: [][]string{{"root", "1061", "1035", "9", "22:36", "?", "00:00:00", "runc init"}}},
		{Titles: []string{"UID", "PID", "PPID", "C", "STIME", "TTY", "TIME", "CMD"},
			Processes: [][]string{{"root", "1061", "1035", "6", "22:36", "?", "00:00:00", "/usr/bin/env bash /usr/local/bin/docker-entrypoint.sh mongod"}}},
		{Titles: []string{"UID", "PID", "PPID", "C", "STIME", "TTY", "TIME", "CMD"},
			Processes: [][]string{{"root", "1061", "1035", "5", "22:36", "?", "00:00:00", "bash /usr/local/bin/docker-entrypoint.sh mongod"}}},
		// The user column on a normal host, which also contains "mongod".
		{Titles: []string{"UID", "PID", "PPID", "C", "STIME", "TTY", "TIME", "CMD"},
			Processes: [][]string{{"mongodb", "1061", "1035", "5", "22:36", "?", "00:00:00", "bash /usr/local/bin/docker-entrypoint.sh mongod"}}},
	}

	m, port := readyMock(t)
	var tops int
	m.ContainerTopFunc = func(ctx context.Context, id string) (dockerclient.Top, error) {
		tops++
		if tops <= len(startupListings) {
			return startupListings[tops-1], nil
		}
		return dockerclient.Top{
			Titles:    []string{"UID", "PID", "PPID", "C", "STIME", "TTY", "TIME", "CMD"},
			Processes: [][]string{{"mongodb", "1061", "1035", "2", "22:36", "?", "00:00:00", "mongod --bind_ip_all"}},
		}, nil
	}

	c, err := mongod.Start(context.Background(), mongod.WithDocker(m), mongod.WithPort(port), mongod.WithStartTimeout(10*time.Second))
	require.NoError(t, err, "mongod does appear eventually, so the start succeeds")
	t.Cleanup(func() { _ = c.Stop(context.Background()) })

	assert.Greater(t, tops, len(startupListings),
		"every listing before the last is the entrypoint shell or the mongodb user, not mongod; treating either as ready hands back a container whose database has not started")
}

func TestReadinessReadsTheCommandColumnByName(t *testing.T) {
	// A column order other than the default ps output, to prove the command
	// is found by its title and not by its position.
	m, port := readyMock(t)
	m.ContainerTopFunc = func(ctx context.Context, id string) (dockerclient.Top, error) {
		return dockerclient.Top{
			Titles:    []string{"COMMAND", "PID", "USER"},
			Processes: [][]string{{"mongod --bind_ip_all", "1", "mongodb"}},
		}, nil
	}

	c, err := mongod.Start(context.Background(), mongod.WithDocker(m), mongod.WithPort(port), mongod.WithStartTimeout(5*time.Second))

	require.NoError(t, err, "the command is in the COMMAND column wherever that column happens to be, and the daemon's ps arguments are not fixed")
	t.Cleanup(func() { _ = c.Stop(context.Background()) })
}

// A container that crashed on startup answers 409 to every top, forever. The
// probe treated that as "not started yet" and waited out the whole budget.
func TestStartFailsAtOnceWhenTheContainerHasExited(t *testing.T) {
	m, _ := readyMock(t)
	dead, err := mongod.GetAvailablePort()
	require.NoError(t, err, "the test needs a port nothing is listening on")
	var inspects int
	m.ContainerInspectFunc = func(ctx context.Context, id string) (dockerclient.ContainerInspect, error) {
		inspects++
		if inspects == 1 {
			// The published-port read, which happens before the probe starts.
			return inspectPublishing(id, dead), nil
		}
		return dockerclient.ContainerInspect{
			ID:    id,
			State: dockerclient.ContainerState{Status: "exited", Running: false, ExitCode: 14},
		}, nil
	}
	m.ContainerTopFunc = func(ctx context.Context, id string) (dockerclient.Top, error) {
		return dockerclient.Top{}, &dockerclient.StatusError{StatusCode: 409, Method: "GET", Path: "/containers/x/top", Message: "Container x is not running"}
	}

	started := time.Now()
	_, err = mongod.Start(context.Background(), mongod.WithDocker(m), mongod.WithStartTimeout(30*time.Second))

	require.Error(t, err, "a container that has exited is never going to answer")
	assert.ErrorIs(t, err, mongod.ErrContainerExited, "callers branch on the sentinel, and a crash is a different thing from a slow start")
	assert.Contains(t, err.Error(), "14", "the exit code is the first thing the reader needs, because it is what the container's logs will explain")
	assert.Less(t, time.Since(started), 5*time.Second, "waiting out a 30 second budget for a container that is already dead wastes the time of whoever is watching the test")
	assert.Len(t, m.CallsTo("ContainerRemove"), 1, "the dead container is still removed")
}

func TestStartFailsAtOnceWhenTheContainerWasRemovedUnderneathIt(t *testing.T) {
	m, _ := readyMock(t)
	dead, err := mongod.GetAvailablePort()
	require.NoError(t, err, "the test needs a port nothing is listening on")
	notFound := &dockerclient.StatusError{StatusCode: 404, Method: "GET", Path: "/containers/x/json", Message: "No such container: x"}
	var inspects int
	m.ContainerInspectFunc = func(ctx context.Context, id string) (dockerclient.ContainerInspect, error) {
		inspects++
		if inspects == 1 {
			return inspectPublishing(id, dead), nil
		}
		return dockerclient.ContainerInspect{}, notFound
	}
	m.ContainerTopFunc = func(ctx context.Context, id string) (dockerclient.Top, error) {
		return dockerclient.Top{}, notFound
	}

	started := time.Now()
	_, err = mongod.Start(context.Background(), mongod.WithDocker(m), mongod.WithStartTimeout(30*time.Second))

	require.Error(t, err, "a container that is gone cannot become ready")
	assert.ErrorIs(t, err, mongod.ErrContainerExited, "something removed the container, which is a state to report rather than to keep polling")
	assert.Less(t, time.Since(started), 5*time.Second, "there is nothing left to wait for once the container does not exist")
}

func TestStartKeepsWaitingWhileTheContainerIsStillStarting(t *testing.T) {
	// The 409 that means "not running yet" looks identical to the one that
	// means "crashed"; only inspect tells them apart, so a container that is
	// merely slow must not be given up on.
	m, port := readyMock(t)
	var tops int
	m.ContainerTopFunc = func(ctx context.Context, id string) (dockerclient.Top, error) {
		tops++
		if tops < 3 {
			return dockerclient.Top{}, &dockerclient.StatusError{StatusCode: 409, Method: "GET", Path: "/containers/x/top", Message: "Container x is not running"}
		}
		return mongodProcesses(), nil
	}
	m.ContainerInspectFunc = func(ctx context.Context, id string) (dockerclient.ContainerInspect, error) {
		info := inspectPublishing(id, port)
		if tops < 3 {
			// Created but not yet running, which is a normal moment during a
			// start and not a failure.
			info.State = dockerclient.ContainerState{Status: "created", Running: false}
		}
		return info, nil
	}

	c, err := mongod.Start(context.Background(), mongod.WithDocker(m), mongod.WithPort(port), mongod.WithStartTimeout(10*time.Second))

	require.NoError(t, err, "a container still being created is not a container that has exited, and giving up on it would make every slow daemon a failure")
	t.Cleanup(func() { _ = c.Stop(context.Background()) })
}

func TestWithStartTimeoutZeroIsRejected(t *testing.T) {
	m := &dockermock.Mock{}

	_, err := mongod.Start(context.Background(), mongod.WithDocker(m), mongod.WithStartTimeout(0))

	require.Error(t, err, "a zero budget is already expired when the wait begins, so every start with it fails after one poll")
	assert.ErrorIs(t, err, dockerclient.ErrInvalidArgument, "a budget that cannot succeed is a bad argument, caught before anything is created")
	assert.Empty(t, m.CallsTo("ContainerCreate"), "nothing is created for a start that cannot finish")
}

// The cleanup that removes a container a failed start created ran on a
// context with no deadline at all, so a daemon that hung on the removal hung
// the caller with it.
func TestFailedStartCleanupDoesNotInheritAnExpiredContext(t *testing.T) {
	m, _ := readyMock(t)
	startErr := &dockerclient.StatusError{StatusCode: 500, Method: "POST", Path: "/containers/x/start", Message: "driver failed"}
	m.ContainerStartFunc = func(ctx context.Context, id string) error { return startErr }
	var removeCtxErr error
	m.ContainerRemoveFunc = func(ctx context.Context, id string, opts dockerclient.RemoveOptions) error {
		removeCtxErr = ctx.Err()
		return nil
	}

	// A context that is already cancelled when the failure happens: the
	// cleanup still has to run, because the container exists either way.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := mongod.Start(ctx, mongod.WithDocker(m), mongod.WithHostIP("127.0.0.1"))

	require.Error(t, err, "the start failed, which is what this test arranges")
	require.Len(t, m.CallsTo("ContainerRemove"), 1, "a cancelled caller still leaves a container behind, and it is still this function's to remove")
	assert.NoError(t, removeCtxErr, "the removal runs on a context of its own; inheriting the cancelled one would make the cleanup fail exactly when it is needed")
}

func TestFailedStartCleanupIsBounded(t *testing.T) {
	m, _ := readyMock(t)
	m.ContainerStartFunc = func(ctx context.Context, id string) error {
		return &dockerclient.StatusError{StatusCode: 500, Method: "POST", Path: "/containers/x/start", Message: "driver failed"}
	}
	var removeDeadline bool
	m.ContainerRemoveFunc = func(ctx context.Context, id string, opts dockerclient.RemoveOptions) error {
		_, removeDeadline = ctx.Deadline()
		return nil
	}

	_, err := mongod.Start(context.Background(), mongod.WithDocker(m), mongod.WithHostIP("127.0.0.1"))

	require.Error(t, err, "the start failed, which is what this test arranges")
	assert.True(t, removeDeadline, "a daemon that stalls on the removal must not turn a failed start into one that never returns, so the cleanup carries a deadline of its own")
}

func TestIntegrationReachableOnANonLoopbackAddress(t *testing.T) {
	docker, ctx := liveDocker(t)
	key, value := runLabel(t)
	host := nonLoopbackAddress(t)

	c, err := mongod.Start(ctx, mongod.WithDocker(docker), mongod.WithHostIP(host), mongod.WithLabel(key, value))
	require.NoError(t, err, "mongod integration: cannot start a container reachable at %s", host)
	t.Cleanup(func() { _ = c.Stop(context.Background()) })

	assert.Equal(t, host, c.Host(), "WithHostIP decides the address, and the container has to actually be reachable there")
	info, err := docker.ContainerInspect(ctx, c.ID())
	require.NoError(t, err, "inspecting the container the test just started must succeed")
	bindings := info.NetworkSettings.Ports["27017/tcp"]
	require.NotEmpty(t, bindings, "27017 must be published")
	assert.Equal(t, "0.0.0.0", bindings[0].HostIP,
		"the daemon's own view is the proof: a port bound to 127.0.0.1 is refused from every other interface, which is what made the sibling-container shape fail")

	conn, err := net.DialTimeout("tcp", net.JoinHostPort(host, strconv.Itoa(c.Port())), 10*time.Second)
	require.NoError(t, err, "the address Start reported has to accept a connection; this is the end-to-end proof that the bind follows the resolution")
	require.NoError(t, conn.Close(), "closing the probe connection must succeed")
}

func TestIntegrationContainerThatDiesBeforeMongodFailsFast(t *testing.T) {
	docker, ctx := liveDocker(t)
	key, value := runLabel(t)

	// A container that runs for a moment and then exits without ever being
	// mongod. Crashing mongod itself does not test this reliably: an
	// unrecognised flag is rejected after the entrypoint has already exec'd
	// mongod, so the process genuinely appears in the listing for an instant
	// and the probe is right to accept it. What has to be detected is the
	// container being gone, which is what this arranges, and which in
	// production is a storage engine that fails a second or two in.
	const budget = 60 * time.Second
	started := time.Now()
	c, err := mongod.Start(ctx,
		mongod.WithDocker(docker),
		mongod.WithMongodArgs("sh", "-c", "sleep 2; exit 3"),
		mongod.WithLabel(key, value),
		mongod.WithStartTimeout(budget),
	)
	// A test that expects a failure still has to clean up after an unexpected
	// success. An earlier version of this test did not, and the one time it
	// unexpectedly passed it left its container behind for hours.
	t.Cleanup(func() { _ = c.Stop(context.Background()) })

	require.Error(t, err, "a container that has exited is never going to serve MongoDB")
	assert.ErrorIs(t, err, mongod.ErrContainerExited, "the container exited, which is a crash to investigate rather than a start to wait longer for")
	assert.False(t, errors.Is(err, context.DeadlineExceeded), "reporting a timeout for a container that died in two seconds sends the reader looking in the wrong place")
	assert.Contains(t, err.Error(), "exit code 3", "the exit code is what the container's logs will explain, so the error has to carry it")
	assert.Less(t, time.Since(started), budget/2, "the exit is noticed when it happens; polling to the end of the budget wastes the time of whoever is waiting")

	if ids, checked := containersLabelled(t, key, value); checked {
		assert.Empty(t, ids, "a container that failed to start is removed like any other failure")
	}
}

// nonLoopbackAddress returns an address of this machine that is not loopback,
// which is the closest a test running on the host can get to the address a
// sibling container would dial.
func nonLoopbackAddress(t *testing.T) string {
	t.Helper()
	addrs, err := net.InterfaceAddrs()
	require.NoError(t, err, "reading this machine's addresses must succeed")
	for _, a := range addrs {
		ipNet, ok := a.(*net.IPNet)
		if !ok || ipNet.IP.IsLoopback() || ipNet.IP.To4() == nil {
			continue
		}
		return ipNet.IP.String()
	}
	require.Fail(t, "this machine has no non-loopback ipv4 address, so the case this test is about cannot be set up here")
	return ""
}
