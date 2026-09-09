package mongod_test

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/tophergopher/mongotest/dockerapi"
	"github.com/tophergopher/mongotest/dockerclient"
	"github.com/tophergopher/mongotest/mongod"
)

// These tests start real containers on a real Docker daemon, located through
// the normal environment. They fail, rather than skip, when no daemon is
// reachable: mongotest exists to start MongoDB containers, and a suite that
// quietly skipped would hide the package being completely broken.
//
// They need the mongo:8 image; pull it once with `docker pull mongo:8`, or
// let the first test pull it.

// integrationTimeout is generous on purpose. On a slow storage driver a
// container can take ten to fifteen seconds to start when several start at
// once, and the first run may also be pulling the image.
const integrationTimeout = 5 * time.Minute

// liveDocker returns a client for the real daemon, or fails with guidance.
func liveDocker(t testing.TB) (dockerclient.Client, context.Context) {
	t.Helper()
	client, err := dockerapi.FromEnv()
	require.NoError(t, err, "mongod integration: cannot configure a Docker client; set DOCKER_HOST to point at a running Docker daemon")
	ctx, cancel := context.WithTimeout(context.Background(), integrationTimeout)
	t.Cleanup(cancel)
	require.NoError(t, client.Negotiate(ctx), "mongod integration: no Docker daemon reachable at %s; set DOCKER_HOST to point at a running Docker daemon", client.Host())
	return client, ctx
}

// runLabel is a label unique to one test, so that the check for containers
// left behind cannot be confused by another suite running at the same time.
func runLabel(t testing.TB) (key, value string) {
	t.Helper()
	name, err := mongod.GetAvailablePort()
	require.NoError(t, err, "the test needs a number nothing else in this run will pick, and a free port is one")
	return "mongotest.run", fmt.Sprintf("%s-%d", strings.ReplaceAll(t.Name(), "/", "_"), name)
}

// containersLabelled asks the docker CLI what is left carrying a label. It
// returns false when the CLI is not on PATH, in which case the caller has to
// make do with what the client itself can see.
func containersLabelled(t testing.TB, key, value string) (ids []string, checked bool) {
	t.Helper()
	out, err := exec.Command("docker", "ps", "-aq", "--filter", "label="+key+"="+value).Output()
	if err != nil {
		return nil, false
	}
	return strings.Fields(string(out)), true
}

func TestIntegrationStartConnectAndStop(t *testing.T) {
	key, value := runLabel(t)
	ctx, cancel := context.WithTimeout(context.Background(), integrationTimeout)
	t.Cleanup(cancel)

	// No WithDocker: this is the default path, all the way through
	// dockerapi.FromEnv finding the daemon on its own.
	c, err := mongod.Start(ctx, mongod.WithLabel(key, value))
	require.NoError(t, err, "mongod integration: cannot start a container; is a Docker daemon reachable and the mongo:8 image available?")
	t.Cleanup(func() { _ = c.Stop(context.Background()) })

	assert.NotEmpty(t, c.ID(), "the daemon assigned an id, and everything afterwards is addressed to it")
	assert.Greater(t, c.Port(), 0, "with no WithPort the daemon assigns the host port, and Start reads it back from inspect")
	assert.Contains(t, c.URI(), "directConnection=true", "the driver must be stopped from discovering a topology a single container does not have")

	host, port, err := c.Endpoint("27017/tcp")
	require.NoError(t, err, "27017/tcp is published, so it has an address")
	conn, err := net.DialTimeout("tcp", net.JoinHostPort(host, strconv.Itoa(port)), 10*time.Second)
	require.NoError(t, err, "the address Start resolved is the one a caller dials; if this fails the resolution is wrong for this environment, which is exactly the failure WithHostIP exists to correct")
	require.NoError(t, conn.Close(), "closing the probe connection must succeed")

	require.NoError(t, c.Stop(ctx), "stopping removes the container")
	require.NoError(t, c.Stop(ctx), "Stop is called from defers as well as explicitly, so the second call must be harmless")

	if ids, checked := containersLabelled(t, key, value); checked {
		assert.Empty(t, ids, "a stopped container is removed and not merely killed, so the daemon is left as this test found it")
	}
}

func TestIntegrationMongodIsReallyServing(t *testing.T) {
	docker, ctx := liveDocker(t)
	key, value := runLabel(t)

	c, err := mongod.Start(ctx, mongod.WithDocker(docker), mongod.WithLabel(key, value))
	require.NoError(t, err, "mongod integration: cannot start a container; is a Docker daemon reachable and the mongo:8 image available?")
	t.Cleanup(func() { _ = c.Stop(context.Background()) })

	// The point of waiting for a mongod process before dialling is that the
	// server is up, not merely that a socket answers. Asking mongod itself is
	// the only thing that actually proves it, and it goes through exec rather
	// than the network, so it stays true even where the address resolution is
	// wrong.
	result, err := docker.Exec(ctx, c.ID(), "mongosh", "--quiet", "--norc", "--eval", "db.runCommand({ping:1}).ok")
	require.NoError(t, err, "running mongosh inside the container must succeed")
	assert.Equal(t, 0, result.ExitCode, "mongosh exits non-zero when it cannot reach mongod; stderr was %q", result.Stderr)
	assert.Equal(t, "1", strings.TrimSpace(result.Stdout), "ping answers ok:1 once mongod is serving, so Start returning before that would be a lie")
}

func TestIntegrationWithPortPublishesExactlyThere(t *testing.T) {
	docker, ctx := liveDocker(t)
	key, value := runLabel(t)
	port, err := mongod.GetAvailablePort()
	require.NoError(t, err, "the test needs a free port to pin")

	c, err := mongod.Start(ctx, mongod.WithDocker(docker), mongod.WithPort(port), mongod.WithLabel(key, value))
	require.NoError(t, err, "mongod integration: cannot start a container on the pinned port %d; is something else bound to it?", port)
	t.Cleanup(func() { _ = c.Stop(context.Background()) })

	assert.Equal(t, port, c.Port(), "WithPort exists for callers who need a port known in advance, so the container has to be on that port and no other")
	assert.Contains(t, c.URI(), ":"+strconv.Itoa(port)+"/", "the URI names the pinned port")

	info, err := docker.ContainerInspect(ctx, c.ID())
	require.NoError(t, err, "inspecting the container the test just started must succeed")
	assert.Equal(t, strconv.Itoa(port), info.HostPort("27017/tcp"), "the daemon's own view has to agree, or the port came from somewhere other than the container")
}

func TestIntegrationReplicaSetContainerRunsWithTheFlag(t *testing.T) {
	docker, ctx := liveDocker(t)
	key, value := runLabel(t)

	c, err := mongod.Start(ctx, mongod.WithDocker(docker), mongod.WithReplicaSet("rs0"), mongod.WithLabel(key, value))
	require.NoError(t, err, "mongod integration: cannot start a replica set container")
	t.Cleanup(func() { _ = c.Stop(context.Background()) })

	top, err := docker.ContainerTop(ctx, c.ID())
	require.NoError(t, err, "listing the container's processes must succeed")
	var command string
	for _, process := range top.Processes {
		for _, field := range process {
			if strings.Contains(field, "mongod") {
				command = field
			}
		}
	}
	require.NotEmpty(t, command, "a container Start returned from has a running mongod in it")
	assert.Contains(t, command, "--replSet rs0", "the replica set flag has to reach mongod itself; the entrypoint prepends the program, so the command is flags only")

	// The set is not initiated here: replSetInitiate needs a MongoDB client,
	// which is the driver layers' job and not this package's.
	// rs.status() throws until the set is initiated, so the code has to be
	// caught rather than read off the result.
	result, err := docker.Exec(ctx, c.ID(), "mongosh", "--quiet", "--norc", "--eval",
		"try { rs.status() } catch (e) { print(e.codeName) }")
	require.NoError(t, err, "running mongosh inside the container must succeed")
	assert.Equal(t, "NotYetInitialized", strings.TrimSpace(result.Stdout),
		"mongod is up and in replica set mode, waiting to be initiated; a standalone server would answer NoReplicationEnabled instead, which is how this tells the flag took effect")
}

func TestIntegrationParallelContainersDoNotCollide(t *testing.T) {
	docker, ctx := liveDocker(t)
	key, value := runLabel(t)
	const containers = 6

	// Pull once up front, so six parallel starts do not each discover the
	// image is missing and pull it at the same time.
	require.NoError(t, docker.ImagePull(ctx, "mongo:8"), "mongod integration: cannot pull mongo:8")

	var mu sync.Mutex
	ports := map[int]string{}
	record := func(t testing.TB, c *mongod.Container) {
		t.Helper()
		mu.Lock()
		defer mu.Unlock()
		previous, taken := ports[c.Port()]
		require.False(t, taken, "container %s was published on port %d, which container %s already has: two containers on one port means one of them is unreachable", c.ID(), c.Port(), previous)
		ports[c.Port()] = c.ID()
	}

	t.Run("subtests", func(t *testing.T) {
		for i := range containers {
			t.Run(fmt.Sprintf("container-%d", i), func(t *testing.T) {
				t.Parallel()
				c, err := mongod.Start(ctx, mongod.WithDocker(docker), mongod.WithLabel(key, value))
				require.NoError(t, err, "six containers starting at once must all come up; a daemon-assigned port is what keeps them from racing for one")
				defer func() { _ = c.Stop(context.Background()) }()
				record(t, c)

				conn, err := net.DialTimeout("tcp", net.JoinHostPort(c.Host(), strconv.Itoa(c.Port())), 10*time.Second)
				require.NoError(t, err, "each container has to be reachable at its own address")
				require.NoError(t, conn.Close(), "closing the probe connection must succeed")

				require.NoError(t, c.Stop(ctx), "each subtest removes the container it started")
			})
		}
	})

	assert.Len(t, ports, containers, "every start must have produced a container on a port of its own")
	if ids, checked := containersLabelled(t, key, value); checked {
		assert.Empty(t, ids, "every container this test started was removed, so the daemon is left as it was found")
	}
}

func TestIntegrationStartFailsWhenTheImageDoesNotExist(t *testing.T) {
	docker, ctx := liveDocker(t)

	_, err := mongod.Start(ctx,
		mongod.WithDocker(docker),
		mongod.WithImage("mongo:this-tag-does-not-exist"),
		mongod.WithStartTimeout(30*time.Second),
	)

	require.Error(t, err, "an image that cannot be pulled cannot be run, and saying so at once beats waiting out the start timeout")
	assert.Contains(t, err.Error(), "mongo:this-tag-does-not-exist", "the message names the image at fault, which is what the reader has to correct")
	assert.False(t, errors.Is(err, context.DeadlineExceeded), "the failure is a missing image, not a slow one; reporting a timeout would send the reader looking in the wrong place")
}
