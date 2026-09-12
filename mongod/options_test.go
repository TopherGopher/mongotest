package mongod_test

import (
	"context"
	"strconv"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/tophergopher/mongotest/dockerclient"
	"github.com/tophergopher/mongotest/dockermock"
	"github.com/tophergopher/mongotest/mongod"
)

func TestDefaultImageIsMongo8(t *testing.T) {
	m, port := readyMock(t)

	c, err := mongod.Start(context.Background(), mongod.WithDocker(m), mongod.WithPort(port))
	require.NoError(t, err, "a start against a fully scripted double must succeed")
	t.Cleanup(func() { _ = c.Stop(context.Background()) })

	assert.Equal(t, "mongo:8", createdConfig(t, m).Image, "mongo:8 is the documented default image, so a caller who passes no option gets it")
	assert.Equal(t, "mongo:8", c.Image(), "Image reports the image the container was created from")
}

func TestMongotestImageEnvOverridesTheDefault(t *testing.T) {
	t.Setenv("MONGOTEST_IMAGE", "mongo:7")
	m, port := readyMock(t)

	c, err := mongod.Start(context.Background(), mongod.WithDocker(m), mongod.WithPort(port))
	require.NoError(t, err, "a start against a fully scripted double must succeed")
	t.Cleanup(func() { _ = c.Stop(context.Background()) })

	assert.Equal(t, "mongo:7", createdConfig(t, m).Image, "MONGOTEST_IMAGE is how CI pins an image without changing code, so it beats the built-in default")
}

func TestWithImageBeatsTheEnvironment(t *testing.T) {
	t.Setenv("MONGOTEST_IMAGE", "mongo:7")
	m, port := readyMock(t)

	c, err := mongod.Start(context.Background(), mongod.WithDocker(m), mongod.WithPort(port), mongod.WithImage("mongo:8.0-noble"))
	require.NoError(t, err, "a start against a fully scripted double must succeed")
	t.Cleanup(func() { _ = c.Stop(context.Background()) })

	assert.Equal(t, "mongo:8.0-noble", createdConfig(t, m).Image, "an explicit option is more specific than an environment variable, so WithImage wins")
}

func TestCreateRequestCarriesLabelsPortsAndCommand(t *testing.T) {
	m, port := readyMock(t)

	c, err := mongod.Start(context.Background(),
		mongod.WithDocker(m),
		mongod.WithPort(port),
		mongod.WithReplicaSet("rs0"),
		mongod.WithMongodArgs("--setParameter", "enableTestCommands=1"),
		mongod.WithLabel("suite", "regression"),
	)
	require.NoError(t, err, "a start against a fully scripted double must succeed")
	t.Cleanup(func() { _ = c.Stop(context.Background()) })

	cfg := createdConfig(t, m)
	assert.Equal(t, "regression", cfg.Labels["mongotest"], "every container carries mongotest=regression so a stray one can be found and removed")
	assert.Equal(t, "regression", cfg.Labels["suite"], "WithLabel adds to the required label rather than replacing the label set")
	assert.Equal(t, []string{"--replSet", "rs0", "--setParameter", "enableTestCommands=1"},
		cfg.Cmd, "the image entrypoint prepends mongod, so the command is flags only: the replica set flag first, then the caller's extra arguments")
	assert.Equal(t, map[string]struct{}{"27017/tcp": {}}, cfg.ExposedPorts, "mongod listens on 27017, and the port has to be exposed before it can be published")
	require.NotNil(t, cfg.HostConfig, "the port bindings live on HostConfig, so it must be sent")
	assert.Equal(t, []dockerclient.PortBinding{{HostIP: "127.0.0.1", HostPort: strconv.Itoa(port)}},
		cfg.HostConfig.PortBindings["27017/tcp"], "WithPort pins the host port, published on loopback so the container is not exposed to the network")
}

func TestDefaultPortBindingLetsTheDaemonChoose(t *testing.T) {
	m, _ := readyMock(t)

	c, err := mongod.Start(context.Background(), mongod.WithDocker(m))
	require.NoError(t, err, "a start against a fully scripted double must succeed")
	t.Cleanup(func() { _ = c.Stop(context.Background()) })

	cfg := createdConfig(t, m)
	require.NotNil(t, cfg.HostConfig, "the port bindings live on HostConfig, so it must be sent")
	assert.Equal(t, []dockerclient.PortBinding{{HostIP: "127.0.0.1", HostPort: ""}},
		cfg.HostConfig.PortBindings["27017/tcp"], "an empty HostPort asks the daemon to choose, which avoids the race between finding a free port and binding it when containers start in parallel")
}

func TestDefaultNameIsGeneratedAndWithNameOverridesIt(t *testing.T) {
	m, port := readyMock(t)

	c, err := mongod.Start(context.Background(), mongod.WithDocker(m), mongod.WithPort(port))
	require.NoError(t, err, "a start against a fully scripted double must succeed")
	t.Cleanup(func() { _ = c.Stop(context.Background()) })

	name := createdName(t, m)
	assert.Regexp(t, `^mongotest-[0-9a-f]{8}$`, name, "the generated name is mongotest- plus eight random hex characters, so parallel starts do not collide on a name")
	assert.Equal(t, name, c.Name(), "Name reports the name the container was created with")

	m.Reset()
	named, err := mongod.Start(context.Background(), mongod.WithDocker(m), mongod.WithPort(port), mongod.WithName("mongotest-fixed"))
	require.NoError(t, err, "a start against a fully scripted double must succeed")
	t.Cleanup(func() { _ = named.Stop(context.Background()) })

	assert.Equal(t, "mongotest-fixed", createdName(t, m), "WithName is how a caller pins a name it can find the container by afterwards")
	assert.Equal(t, "mongotest-fixed", named.Name(), "Name reports the name the caller asked for")
}

func TestWithReplicaSetIsReportedBack(t *testing.T) {
	m, port := readyMock(t)

	c, err := mongod.Start(context.Background(), mongod.WithDocker(m), mongod.WithPort(port), mongod.WithReplicaSet("rs0"))
	require.NoError(t, err, "a start against a fully scripted double must succeed")
	t.Cleanup(func() { _ = c.Stop(context.Background()) })

	assert.Equal(t, "rs0", c.ReplicaSet(), "the driver layers read ReplicaSet to decide whether to run replSetInitiate, so it must report what was asked for")
}

func TestWithoutReplicaSetTheCommandIsEmpty(t *testing.T) {
	m, port := readyMock(t)

	c, err := mongod.Start(context.Background(), mongod.WithDocker(m), mongod.WithPort(port))
	require.NoError(t, err, "a start against a fully scripted double must succeed")
	t.Cleanup(func() { _ = c.Stop(context.Background()) })

	assert.Empty(t, createdConfig(t, m).Cmd, "with no replica set and no extra arguments there is nothing to override, so the image's own command must be left alone")
	assert.Empty(t, c.ReplicaSet(), "a standalone container has no replica set name to report")
}

func TestWithTLSMarksTheURI(t *testing.T) {
	m, port := readyMock(t)

	c, err := mongod.Start(context.Background(), mongod.WithDocker(m), mongod.WithPort(port), mongod.WithTLS())
	require.NoError(t, err, "a start against a fully scripted double must succeed")
	t.Cleanup(func() { _ = c.Stop(context.Background()) })

	assert.Equal(t, "mongodb://127.0.0.1:"+strconv.Itoa(port)+"/?directConnection=true&tls=true",
		c.URI(), "a driver connecting to a TLS-mode server has to be told so in the URI, or the handshake fails with an unhelpful error")
}

func TestWithLoggerReceivesTheStartEvents(t *testing.T) {
	m, port := readyMock(t)
	logger := &recordingLogger{}

	c, err := mongod.Start(context.Background(), mongod.WithDocker(m), mongod.WithPort(port), mongod.WithLogger(logger))
	require.NoError(t, err, "a start against a fully scripted double must succeed")
	t.Cleanup(func() { _ = c.Stop(context.Background()) })

	assert.NotEmpty(t, logger.Messages(), "WithLogger exists so a caller can see what a start did; a logger that is never written to is the same as no logger")
}

func TestWithStartTimeoutBoundsTheWait(t *testing.T) {
	m, _ := readyMock(t)
	// Nothing is listening on this port, so readiness can never succeed and
	// the only thing that ends the wait is the timeout.
	dead, err := mongod.GetAvailablePort()
	require.NoError(t, err, "the test needs a port that is free, so that the readiness probe finds nothing listening")
	m.ContainerInspectFunc = func(ctx context.Context, id string) (dockerclient.ContainerInspect, error) {
		return inspectPublishing(id, dead), nil
	}

	started := time.Now()
	_, err = mongod.Start(context.Background(), mongod.WithDocker(m), mongod.WithStartTimeout(150*time.Millisecond))
	require.Error(t, err, "nothing is listening on the published port, so the start cannot succeed")
	assert.Less(t, time.Since(started), 5*time.Second, "WithStartTimeout is what bounds the wait; without it honoured the start would run to the 60 second default")
}

func TestUnknownOptionValuesAreRejectedBeforeAnythingIsCreated(t *testing.T) {
	cases := []struct {
		name   string
		option *mongod.Options
		fix    string
	}{
		{name: "port below the valid range", option: mongod.WithPort(-1), fix: "a negative port cannot be bound"},
		{name: "port above the valid range", option: mongod.WithPort(70000), fix: "a port above 65535 cannot be bound"},
		{name: "negative start timeout", option: mongod.WithStartTimeout(-time.Second), fix: "a negative timeout would expire before the container is created"},
		{name: "empty image", option: mongod.WithImage(""), fix: "an empty image reference names nothing"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m := &dockermock.Mock{}

			_, err := mongod.Start(context.Background(), mongod.WithDocker(m), tc.option)

			require.Error(t, err, tc.fix)
			assert.ErrorIs(t, err, dockerclient.ErrInvalidArgument, "a rejected option is a client-side argument problem, which callers branch on with errors.Is")
			assert.Empty(t, m.CallsTo("ContainerCreate"), "a bad option is caught before the daemon is asked to create anything, so there is nothing to clean up")
		})
	}
}
