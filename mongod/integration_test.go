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

// requireImage makes sure an image is available locally, pulling it only if
// it is not.
func requireImage(t testing.TB, ctx context.Context, docker dockerclient.Client, ref string) {
	t.Helper()
	if _, err := docker.ImageInspect(ctx, ref); err == nil {
		return
	}
	require.NoError(t, docker.ImagePull(ctx, ref), "mongod integration: cannot pull %s", ref)
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
	requirePingEventually(t, ctx, docker, c.ID(), 60*time.Second)
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

	// Make sure the image is present up front, so six parallel starts do not
	// each discover it missing and pull it at the same time. Inspect first:
	// pulling an image that is already local is a registry round trip for
	// nothing, and a rate-limited registry would fail a run that needed
	// nothing from it.
	requireImage(t, ctx, docker, "mongo:8")

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

// The well-known images are only worth having if they resolve to something
// that really exists and really runs. These start each of them for real.
func TestIntegrationWellKnownImagesStart(t *testing.T) {
	cases := []struct {
		name  string
		image mongod.MongoImage
		// replicaSet is left empty for an image that will not accept the flag.
		replicaSet string
	}{
		{name: "docker hub official", image: mongod.ImageDockerHub, replicaSet: "rs0"},
		{name: "mongodb community server", image: mongod.ImageCommunity, replicaSet: "rs0"},
		{name: "atlas local", image: mongod.ImageAtlasLocal},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			docker, ctx := liveDocker(t)
			key, value := runLabel(t)
			requireImage(t, ctx, docker, tc.image.Reference())

			opts := mongod.WithDocker(docker).WithMongoImage(tc.image).WithLabel(key, value)
			if tc.replicaSet != "" {
				opts = opts.WithReplicaSet(tc.replicaSet)
			}
			c, err := mongod.Start(ctx, opts)
			require.NoError(t, err, "mongod integration: %s must start; a well-known image constant that does not resolve to a runnable image is worse than none", tc.image)
			t.Cleanup(func() { _ = c.Stop(context.Background()) })

			assert.Equal(t, tc.image.Reference(), c.Image(), "the container runs the image the constant names")

			// Exec is the control: it reaches mongod even where the network
			// path would not, so this proves the server is really serving
			// rather than that a port happens to be bound.
			requirePingEventually(t, ctx, docker, c.ID(), 90*time.Second)

			require.NoError(t, c.Stop(ctx), "each case removes the container it started")
			if ids, checked := containersLabelled(t, key, value); checked {
				assert.Empty(t, ids, "nothing is left behind")
			}
		})
	}
}

// Atlas Local running a replica set of its own is why ReplicaSetPreconfigured
// exists and why WithReplicaSet is refused for it. This is where that claim is
// checked against the real image rather than taken on trust.
func TestIntegrationAtlasLocalAlreadyRunsAReplicaSet(t *testing.T) {
	docker, ctx := liveDocker(t)
	key, value := runLabel(t)
	requireImage(t, ctx, docker, mongod.ImageAtlasLocal.Reference())

	c, err := mongod.Start(ctx, mongod.WithDocker(docker).WithMongoImage(mongod.ImageAtlasLocal).WithLabel(key, value))
	require.NoError(t, err, "mongod integration: Atlas Local must start with no flags at all")
	t.Cleanup(func() { _ = c.Stop(context.Background()) })

	// Polled rather than asked once, because Atlas Local restarts mongod part
	// way through its startup; see requireEvalEventually.
	setName := requireEvalEventually(t, ctx, docker, c.ID(), "rs.status().set",
		func(got string) bool { return got != "" && !strings.Contains(got, "Error") }, 120*time.Second)

	assert.NotEmpty(t, setName,
		"Atlas Local initiates a single-node set while starting, so rs.status() answers with a name instead of throwing NotYetInitialized")
	assert.NotEqual(t, "rs0", setName,
		"the name is generated from the container's hostname rather than being a fixed one, which is why WithReplicaSet cannot be honoured for this image and a driver layer has to read the name")
	assert.True(t, c.MongoImage().ReplicaSetPreconfigured(),
		"and the image says so in advance, which is what lets Start refuse WithReplicaSet with an explanation instead of producing a container that cannot exec")
}

// Atlas Local's command is its entrypoint, so mongod flags replace the program
// rather than configuring it. Validation refuses that combination; this proves
// the refusal is warranted by showing what the container would otherwise do.
func TestIntegrationAtlasLocalWouldDieIfGivenMongodFlags(t *testing.T) {
	docker, ctx := liveDocker(t)
	key, value := runLabel(t)
	requireImage(t, ctx, docker, mongod.ImageAtlasLocal.Reference())

	// Start refuses this before the daemon is asked for anything.
	_, err := mongod.Start(ctx, mongod.WithDocker(docker).WithMongoImage(mongod.ImageAtlasLocal).WithMongodArgs("--quiet"))
	require.ErrorIs(t, err, mongod.ErrUnsupportedForImage, "the combination is refused rather than attempted")

	// What it is protecting against: the same request made directly.
	id, _, err := docker.ContainerCreate(ctx, "", dockerclient.ContainerConfig{
		Image:  mongod.ImageAtlasLocal.Reference(),
		Cmd:    []string{"--quiet"},
		Labels: map[string]string{key: value},
	})
	require.NoError(t, err, "the daemon accepts the create; nothing is wrong until the container tries to run")
	t.Cleanup(func() {
		_ = docker.ContainerRemove(context.Background(), id, dockerclient.RemoveOptions{Force: true, RemoveVolumes: true})
	})

	startErr := docker.ContainerStart(ctx, id)
	require.Error(t, startErr, "the flag has replaced the program, so there is nothing to execute")
	assert.Contains(t, startErr.Error(), "--quiet",
		"the daemon's complaint is that the flag is not an executable, which says nothing about the option that caused it; that is why validation catches it first")
}

// requireEvalEventually runs a mongosh expression inside the container until
// its output satisfies want, or fails the test.
//
// Polling here is the contract, not a workaround for flakiness, and there are
// two separate reasons for it. Start proves that mongod's process exists, that
// the container is alive and that the published address answers, and
// deliberately not that mongod is accepting MongoDB connections: docker-proxy
// accepts on its behalf and no amount of dialling can see past it. That gap is
// easy to see with a slower image, where mongodb-community-server answered
// ECONNREFUSED on the first attempt while the official image was already
// serving.
//
// The second reason is Atlas Local, which restarts mongod during its own
// startup. Measured on mongodb/mongodb-atlas-local:8.0.28: mongod came up and
// served from roughly six seconds, disappeared at eighteen, and a different
// process was serving by twenty. A test that pinged once and then asserted
// would pass or fail depending on which side of that it landed, so the
// assertion itself is what gets polled.
func requireEvalEventually(t testing.TB, ctx context.Context, docker dockerclient.Client, id, script string, want func(string) bool, timeout time.Duration) string {
	t.Helper()
	deadline := time.Now().Add(timeout)
	var last string
	for attempt := 1; ; attempt++ {
		result, err := docker.Exec(ctx, id, "mongosh", "--quiet", "--norc", "--eval", script)
		require.NoError(t, err, "running mongosh inside the container must succeed; this is the exec path, not the network one")
		got := strings.TrimSpace(result.Stdout)
		if want(got) {
			return got
		}
		last = strings.TrimSpace(got + " " + result.Stderr)
		if time.Now().After(deadline) {
			require.Failf(t, "mongod never gave the expected answer",
				"after %d attempts over %s, %q still answered %q. Exec itself works, so this is mongod rather than a network path",
				attempt, timeout, script, last)
		}
		time.Sleep(250 * time.Millisecond)
	}
}

// requirePingEventually waits until mongod answers a ping.
func requirePingEventually(t testing.TB, ctx context.Context, docker dockerclient.Client, id string, timeout time.Duration) {
	t.Helper()
	requireEvalEventually(t, ctx, docker, id, "db.runCommand({ping:1}).ok", func(got string) bool { return got == "1" }, timeout)
}
