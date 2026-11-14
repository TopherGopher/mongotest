package mongod_test

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/tophergopher/mongotest/dockerclient"
	"github.com/tophergopher/mongotest/dockermock"
	"github.com/tophergopher/mongotest/mongod"
)

func TestStartWithNoOptions(t *testing.T) {
	m, port := readyMock(t)

	c := mustStart(t, m)

	request := created(t, m)
	binding := request.HostConfig.PortBindings["27017/tcp"][0]
	assert.Equal(t, "mongo:8.0", request.Image, "the default image")
	assert.Equal(t, "mongo:8.0", c.Image(), "which the container reports")
	assert.Equal(t, "127.0.0.1", binding.HostIP, "a local container is not published to the network")
	assert.Empty(t, binding.HostPort, "the daemon chooses the port, so parallel starts cannot collide")
	assert.Equal(t, port, c.Port(), "and the container reports the one it chose")
	assert.Equal(t, "127.0.0.1", c.Host(), "where the port is published")
	assert.Equal(t, mongod.RequiredLabelValue, request.Labels[mongod.RequiredLabelKey], "every container can be found by its label")
	assert.Equal(t, createdName(t, m), c.Name(), "the container reports its generated name")
	assert.Equal(t, containerID, c.ID(), "and the id the daemon gave it")
	assert.Empty(t, request.Cmd, "mongod gets no extra flags")
}

func TestGeneratedNamesDiffer(t *testing.T) {
	first, _ := readyMock(t)
	second, _ := readyMock(t)
	mustStart(t, first)
	mustStart(t, second)

	assert.NotEqual(t, createdName(t, first), createdName(t, second), "parallel starts must not ask for the same name")
}

func TestStartPullsAnImageTheDaemonDoesNotHave(t *testing.T) {
	m, _ := readyMock(t)
	creates := 0
	m.ContainerCreateFunc = func(context.Context, string, dockerclient.ContainerConfig) (string, []string, error) {
		creates++
		if creates == 1 {
			return "", nil, dockerclient.ErrNotFound
		}
		return containerID, nil, nil
	}

	_, err := start(t, m)

	require.NoError(t, err, "a missing image is pulled, not reported")
	require.Len(t, m.CallsTo("ImagePull"), 1, "once")
	assert.Equal(t, "mongo:8.0", m.CallsTo("ImagePull")[0].Args[0], "the image that was asked for")
}

func TestStartNamesTheStepThatFailed(t *testing.T) {
	daemonSaidNo := errors.New("the daemon said no")
	cases := []struct {
		op      string
		arrange func(*dockermock.Mock)
	}{
		{mongod.OpPullImage, func(m *dockermock.Mock) {
			m.ContainerCreateFunc = func(context.Context, string, dockerclient.ContainerConfig) (string, []string, error) {
				return "", nil, dockerclient.ErrNotFound
			}
			m.ImagePullFunc = func(context.Context, string) error { return daemonSaidNo }
		}},
		{mongod.OpCreate, func(m *dockermock.Mock) {
			m.ContainerCreateFunc = func(context.Context, string, dockerclient.ContainerConfig) (string, []string, error) {
				return "", nil, daemonSaidNo
			}
		}},
		{mongod.OpStart, func(m *dockermock.Mock) {
			m.ContainerStartFunc = func(context.Context, string) error { return daemonSaidNo }
		}},
		{mongod.OpInspect, func(m *dockermock.Mock) {
			m.ContainerInspectFunc = func(context.Context, string) (dockerclient.ContainerInspect, error) {
				return dockerclient.ContainerInspect{}, daemonSaidNo
			}
		}},
	}
	for _, tc := range cases {
		t.Run(tc.op, func(t *testing.T) {
			m, _ := readyMock(t)
			tc.arrange(m)

			_, err := start(t, m)

			var startErr *mongod.StartError
			require.ErrorAs(t, err, &startErr, "a failed step is a StartError")
			assert.Equal(t, tc.op, startErr.Op, "naming the step")
			assert.ErrorIs(t, err, daemonSaidNo, "and wrapping the daemon's own error")
		})
	}
}

func TestAFailedStartRemovesItsContainer(t *testing.T) {
	m, _ := readyMock(t)
	m.ContainerStartFunc = func(context.Context, string) error { return errors.New("no") }

	_, err := start(t, m)

	require.Error(t, err, "the start fails")
	assert.Len(t, m.CallsTo("ContainerRemove"), 1, "and does not leave the container it created")
}

func TestAFailedStartRemovesItsContainerEvenWhenTheContextIsWhatFailed(t *testing.T) {
	m, _ := readyMock(t)
	ctx, cancel := context.WithCancel(context.Background())
	m.ContainerStartFunc = func(context.Context, string) error {
		cancel()
		return context.Canceled
	}
	removed, live, bounded := false, false, false
	m.ContainerRemoveFunc = func(ctx context.Context, _ string, _ dockerclient.RemoveOptions) error {
		removed, live = true, ctx.Err() == nil
		_, bounded = ctx.Deadline()
		return nil
	}

	_, err := mongod.Start(ctx, mongod.WithDocker(m), mongod.WithHostIP("127.0.0.1"))

	require.Error(t, err, "the start fails")
	require.True(t, removed, "the container is still removed")
	assert.True(t, live, "on a context that is not the cancelled one")
	assert.True(t, bounded, "and that has a deadline, so a stalled daemon cannot hang the caller")
}

func TestStartFailsWhenTheDaemonPublishedNoPort(t *testing.T) {
	m, _ := readyMock(t)
	m.ContainerInspectFunc = func(context.Context, string) (dockerclient.ContainerInspect, error) {
		return dockerclient.ContainerInspect{State: dockerclient.ContainerState{Running: true}}, nil
	}

	_, err := start(t, m)

	assert.ErrorIs(t, err, mongod.ErrNoPublishedPort, "a container with nowhere to connect to is not returned")
}

func TestStartTimesOutWhenNothingAnswers(t *testing.T) {
	m, _ := readyMock(t)
	dead := closedPort(t)
	m.ContainerInspectFunc = func(context.Context, string) (dockerclient.ContainerInspect, error) {
		return running(dead), nil
	}

	_, err := start(t, m, mongod.WithStartTimeout(100*time.Millisecond))

	require.ErrorIs(t, err, mongod.ErrNotReady, "a mongod nothing can connect to is not ready")
	assert.ErrorIs(t, err, context.DeadlineExceeded, "and the error says the time ran out")
	var notReady *mongod.NotReadyError
	require.ErrorAs(t, err, &notReady, "with the details")
	assert.Equal(t, dead, notReady.Port, "including the port that was dialled")
	assert.Equal(t, "WithHostIP", notReady.HostSource, "and what chose the address")
	assert.Len(t, m.CallsTo("ContainerRemove"), 1, "the container is removed")
}

func TestStartStopsWaitingWhenTheCallerCancels(t *testing.T) {
	m, _ := readyMock(t)
	m.ContainerTopFunc = func(context.Context, string) (dockerclient.Top, error) { return processes("runc init"), nil }
	ctx, cancel := context.WithCancel(context.Background())
	time.AfterFunc(50*time.Millisecond, cancel)

	started := time.Now()
	_, err := mongod.Start(ctx, mongod.WithDocker(m), mongod.WithHostIP("127.0.0.1"))

	require.ErrorIs(t, err, context.Canceled, "the caller's context ends the wait")
	assert.Less(t, time.Since(started), 10*time.Second, "long before the start timeout")
}

func TestAnAddressFromTheEnvironmentIsReportedAsComingFromIt(t *testing.T) {
	t.Setenv(mongod.EnvHostIP, "127.0.0.1")
	m, _ := readyMock(t)
	m.ContainerTopFunc = func(context.Context, string) (dockerclient.Top, error) { return processes("runc init"), nil }

	_, err := mongod.Start(context.Background(), mongod.WithDocker(m), mongod.WithStartTimeout(50*time.Millisecond))

	var notReady *mongod.NotReadyError
	require.ErrorAs(t, err, &notReady, "the start times out")
	assert.Equal(t, mongod.EnvHostIP, notReady.HostSource, "the error names the variable, so a wrong address can be found")
}

func TestStartFailsAtOnceWhenTheContainerHasExited(t *testing.T) {
	cases := []struct {
		status   string
		exitCode int
		inspect  func() (dockerclient.ContainerInspect, error)
	}{
		{"exited", 14, func() (dockerclient.ContainerInspect, error) {
			return dockerclient.ContainerInspect{State: dockerclient.ContainerState{Status: "exited", ExitCode: 14}}, nil
		}},
		{"removed", 0, func() (dockerclient.ContainerInspect, error) {
			return dockerclient.ContainerInspect{}, dockerclient.ErrNotFound
		}},
	}
	for _, tc := range cases {
		t.Run(tc.status, func(t *testing.T) {
			m, port := readyMock(t)
			inspects := 0
			m.ContainerInspectFunc = func(context.Context, string) (dockerclient.ContainerInspect, error) {
				inspects++
				if inspects == 1 {
					return running(port), nil // the start's own look at the ports
				}
				return tc.inspect()
			}
			m.ContainerTopFunc = func(context.Context, string) (dockerclient.Top, error) {
				return dockerclient.Top{}, errors.New("container is not running")
			}

			started := time.Now()
			_, err := start(t, m)

			require.ErrorIs(t, err, mongod.ErrContainerExited, "a container that is gone will never be ready")
			var exited *mongod.ContainerExitedError
			require.ErrorAs(t, err, &exited, "with the details")
			assert.Equal(t, tc.status, exited.Status, "what happened to it")
			assert.Equal(t, tc.exitCode, exited.ExitCode, "and its exit code")
			assert.Less(t, time.Since(started), 10*time.Second, "without waiting out the start timeout")
		})
	}
}

func TestStartKeepsWaitingWhileTheContainerIsStillStarting(t *testing.T) {
	m, _ := readyMock(t)
	tops := 0
	m.ContainerTopFunc = func(context.Context, string) (dockerclient.Top, error) {
		tops++
		if tops < 3 {
			return dockerclient.Top{}, errors.New("container is not running")
		}
		return processes("mongod --bind_ip_all"), nil
	}

	_, err := start(t, m)

	assert.NoError(t, err, "a container that has not started yet is not a failure")
}

func TestANilOptionIsIgnored(t *testing.T) {
	m, _ := readyMock(t)

	_, err := start(t, m, nil)

	assert.NoError(t, err, "a nil option, as a conditional one may be, changes nothing")
}

func TestStopRemovesTheContainerAndItsVolumes(t *testing.T) {
	m, _ := readyMock(t)
	c := mustStart(t, m)

	require.NoError(t, c.Stop(context.Background()), "stopping succeeds")
	require.NoError(t, c.Stop(context.Background()), "and is safe to do twice")

	removals := m.CallsTo("ContainerRemove")
	require.Len(t, removals, 1, "the container is removed once")
	assert.Equal(t, dockerclient.RemoveOptions{Force: true, RemoveVolumes: true}, removals[0].Args[1], "running or not, with its volumes")
}

func TestStopOnANilContainerDoesNothing(t *testing.T) {
	var c *mongod.Container

	assert.NoError(t, c.Stop(context.Background()), "a deferred Stop after a failed Start must not panic")
}

func TestStopTreatsAContainerThatIsAlreadyGoneAsStopped(t *testing.T) {
	m, _ := readyMock(t)
	m.ContainerRemoveFunc = func(context.Context, string, dockerclient.RemoveOptions) error {
		return dockerclient.ErrNotFound
	}
	c := mustStart(t, m)

	assert.NoError(t, c.Stop(context.Background()), "something else removing it is not a failure")
}

func TestAStopTheDaemonRefusedCanBeTriedAgain(t *testing.T) {
	m, _ := readyMock(t)
	refused := errors.New("the daemon refused")
	attempts := 0
	m.ContainerRemoveFunc = func(context.Context, string, dockerclient.RemoveOptions) error {
		attempts++
		if attempts == 1 {
			return refused
		}
		return nil
	}
	c := mustStart(t, m)

	assert.ErrorIs(t, c.Stop(context.Background()), refused, "the refusal is reported")
	assert.NoError(t, c.Stop(context.Background()), "and the next Stop tries again")
}

func TestStopGivesUpAtItsDeadlineWhileAnotherStopIsStuck(t *testing.T) {
	m, _ := readyMock(t)
	entered, release := make(chan struct{}), make(chan struct{})
	m.ContainerRemoveFunc = func(context.Context, string, dockerclient.RemoveOptions) error {
		close(entered)
		<-release
		return nil
	}
	c := mustStart(t, m)
	t.Cleanup(func() { close(release) })

	go func() { _ = c.Stop(context.Background()) }() // a test's own Stop, against a stalled daemon
	<-entered

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	stopped := make(chan error, 1)
	go func() { stopped <- c.Stop(ctx) }() // the reaper's, on a signal

	select {
	case err := <-stopped:
		assert.ErrorIs(t, err, context.DeadlineExceeded, "a Stop waiting behind another gives up when its context ends")
	case <-time.After(3 * time.Second):
		assert.Fail(t, "Stop ignored its deadline, so a reap on a signal could hang a dying process")
	}
}

func TestURIWithTLS(t *testing.T) {
	m, _ := readyMock(t)

	c := mustStart(t, m, mongod.WithTLS())

	assert.Equal(t, "true", parseURI(t, c).Query().Get("tls"), "a driver has to be told to use TLS")
}

func TestURIAsksForADirectConnection(t *testing.T) {
	m, port := readyMock(t)

	c := mustStart(t, m)

	uri := parseURI(t, c)
	assert.Equal(t, "mongodb", uri.Scheme, "a MongoDB connection string")
	assert.Equal(t, fmt.Sprintf("127.0.0.1:%d", port), uri.Host, "to the published address")
	assert.Equal(t, "true", uri.Query().Get("directConnection"), "so a driver does not follow a member's own address, which only works inside the container")
}
