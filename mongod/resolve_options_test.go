package mongod_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/tophergopher/mongotest/dockerclient"
	"github.com/tophergopher/mongotest/dockermock"
	"github.com/tophergopher/mongotest/mongod"
)

// Options are a plain value a caller can build three ways: chained from a
// package-level helper, chained from NewOptions, or written as a struct
// literal. All three have to reach the same container.

func TestOptionsChainFromAPackageHelper(t *testing.T) {
	m, port := readyMock(t)

	c, err := mongod.Start(context.Background(),
		mongod.WithDocker(m).WithPort(port).WithReplicaSet("rs0").WithLabel("suite", "checkout"))
	require.NoError(t, err, "a start against a fully scripted double must succeed")
	t.Cleanup(func() { _ = c.Stop(context.Background()) })

	cfg := createdConfig(t, m)
	assert.Equal(t, []string{"--replSet", "rs0"}, cfg.Cmd, "every link in the chain has to survive into the create request")
	assert.Equal(t, "checkout", cfg.Labels["suite"], "a label set part-way along the chain is kept")
	assert.Equal(t, port, c.Port(), "the port set part-way along the chain is the one published")
}

func TestOptionsChainFromNewOptions(t *testing.T) {
	m, port := readyMock(t)

	c, err := mongod.Start(context.Background(),
		mongod.NewOptions().WithDocker(m).WithPort(port).WithImage("8.0"))
	require.NoError(t, err, "a start against a fully scripted double must succeed")
	t.Cleanup(func() { _ = c.Stop(context.Background()) })

	assert.Equal(t, "mongo:8.0", c.Image(), "NewOptions is the explicit way to start a chain and must behave exactly like the package-level helpers")
}

func TestOptionsAsAStructLiteral(t *testing.T) {
	m, port := readyMock(t)

	c, err := mongod.Start(context.Background(), &mongod.Options{
		Docker: m,
		Port:   port,
		Image:  mongod.NewMongoImage("", "mongo", "8.0.30"),
		Labels: map[string]string{"suite": "checkout"},
	})
	require.NoError(t, err, "a start against a fully scripted double must succeed")
	t.Cleanup(func() { _ = c.Stop(context.Background()) })

	assert.Equal(t, "mongo:8.0.30", c.Image(), "the fields are public so that a caller can write the whole thing out, which is what a table-driven test wants")
	assert.Equal(t, "checkout", createdConfig(t, m).Labels["suite"], "a label map given as a field reaches the container")
	assert.Equal(t, "regression", createdConfig(t, m).Labels["mongotest"], "the required label is added to a caller's map rather than being replaced by it")
}

func TestSeveralOptionsAreMergedInOrder(t *testing.T) {
	m, port := readyMock(t)

	// The old call style, and the way a test composes a shared base with a
	// per-case override.
	base := mongod.NewOptions().WithDocker(m).WithImage("8.0")
	c, err := mongod.Start(context.Background(), base, mongod.WithPort(port).WithImage("8.0.30"))
	require.NoError(t, err, "a start against a fully scripted double must succeed")
	t.Cleanup(func() { _ = c.Stop(context.Background()) })

	assert.Equal(t, "mongo:8.0.30", c.Image(), "a later Options overrides an earlier one field by field, so a shared base can be specialised")
	assert.Equal(t, port, c.Port(), "a field only the later Options sets is still applied")
}

func TestMergingLeavesTheCallersOptionsAlone(t *testing.T) {
	m, port := readyMock(t)
	base := mongod.NewOptions().WithDocker(m).WithVersion("8.0").WithLabel("suite", "checkout")

	c, err := mongod.Start(context.Background(), base,
		mongod.WithPort(port).WithVersion("8.0.30").WithLabel("case", "two"))
	require.NoError(t, err, "a start against a fully scripted double must succeed")
	t.Cleanup(func() { _ = c.Stop(context.Background()) })

	assert.Equal(t, "8.0.30", c.MongoImage().Version, "the later Options wins for the container that was started")
	assert.Equal(t, "8.0", base.Image.Version,
		"a base reused across table cases must not be mutated by a start that specialised it, or case two inherits case one's overrides")
	assert.NotContains(t, base.Labels, "case",
		"the label maps are copied too; sharing one would let a case add a label every later case then carries")
}

// WithImage parses as it is called, so the public fields hold the answer
// immediately. A reader of Options sees one representation of the image and
// never has to know whether it has been split yet.
func TestWithImagePopulatesThePartsImmediately(t *testing.T) {
	opts := mongod.WithImage("public.ecr.aws/docker/library/mongo:8.0")

	assert.Equal(t, "public.ecr.aws", opts.Image.Registry, "the registry is available before anything is resolved")
	assert.Equal(t, "docker/library/mongo", opts.Image.Repository, "so is the repository path")
	assert.Equal(t, "8.0", opts.Image.Version, "and so is the version")
	assert.Equal(t, "public.ecr.aws/docker/library/mongo:8.0", opts.Image.Reference(),
		"which means the image can be read back, logged or asserted on without calling Resolve at all")
}

// It sets only the parts the reference actually names, exactly as the
// individual helpers do, so that whatever the reference is silent about is
// still filled in from the environment and the defaults.
func TestWithImageSetsOnlyThePartsTheReferenceNames(t *testing.T) {
	cases := []struct {
		name       string
		ref        string
		registry   string
		repository string
		version    string
	}{
		{name: "a version alone", ref: "8.0", version: "8.0"},
		{name: "a repository alone", ref: "mongo", repository: "mongo"},
		{name: "both", ref: "mongo:8.0", repository: "mongo", version: "8.0"},
		{name: "a registry and repository", ref: "public.ecr.aws/docker/library/mongo", registry: "public.ecr.aws", repository: "docker/library/mongo"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			opts := mongod.WithImage(tc.ref)

			assert.Equal(t, tc.registry, opts.Image.Registry, "a part the reference does not name is left alone for the environment and the defaults to fill in")
			assert.Equal(t, tc.repository, opts.Image.Repository, "same for the repository")
			assert.Equal(t, tc.version, opts.Image.Version, "same for the version")
		})
	}
}

func TestWithImageComposesWithAKnownImage(t *testing.T) {
	// Naming a version alone against a known image keeps where that image
	// comes from, which is the same rule the separate helpers follow.
	resolved, err := mongod.WithMongoImage(mongod.ImageCommunity).WithImage("8.0-ubi8").Resolve()

	require.NoError(t, err, "the combination is valid")
	assert.Equal(t, "mongodb/mongodb-community-server:8.0-ubi8", resolved.Image.Reference(),
		"WithImage sets the parts its reference names and no others, so a bare version behaves exactly like WithVersion")
}

// Parsing eagerly means a bad reference is known at once, but the helpers
// return *Options so there is nothing to return it on. The failure is kept and
// reported by Resolve, which is where every other problem with a
// configuration surfaces too.
func TestWithImageReportsABadReferenceFromResolve(t *testing.T) {
	opts := mongod.WithImage("Mongo:8")

	_, err := opts.Resolve()

	require.Error(t, err, "an uppercase repository is not a reference the daemon would accept")
	assert.ErrorIs(t, err, dockerclient.ErrInvalidArgument, "callers branch on the sentinel")
	assert.Contains(t, err.Error(), "Mongo:8", "the message names the value at fault, which is the whole point of keeping it")
}

func TestABadReferenceFailsTheStartBeforeAnythingIsCreated(t *testing.T) {
	m := &dockermock.Mock{}

	_, err := mongod.Start(context.Background(), mongod.WithDocker(m).WithImage("Mongo:8"))

	require.Error(t, err, "a start cannot proceed with an image reference the daemon would refuse")
	assert.ErrorIs(t, err, dockerclient.ErrInvalidArgument, "callers branch on the sentinel")
	assert.Empty(t, m.CallsTo("ContainerCreate"), "nothing is created for a configuration that is known to be wrong")
}

func TestTheFirstBadReferenceIsTheOneReported(t *testing.T) {
	// Two mistakes in one chain: the reader is told about the first, because
	// that is the one they wrote first and the rest may well follow from it.
	_, err := mongod.WithImage("Mongo:8").WithImage("also bad:@@").Resolve()

	require.Error(t, err, "the chain carries its failures forward rather than losing them")
	assert.Contains(t, err.Error(), "Mongo:8", "the earliest failure is the one to act on")
}

// A reference and the individual helpers combine, because a caller who uses
// both usually means to override one part of the other.
func TestTheSeparateHelpersOverrideAReference(t *testing.T) {
	resolved, err := mongod.WithImage("mongo:8").WithRegistry(mongod.RegistryPublicECR).WithRepository(mongod.RepositoryPublicECRMirror).Resolve()

	require.NoError(t, err, "the combination is valid")
	assert.Equal(t, "public.ecr.aws/docker/library/mongo:8", resolved.Image.Reference(),
		"moving a pinned image to the ECR mirror means keeping the version and replacing where it comes from, which is exactly what the separate parts are for")
}

func TestPrecedenceIsOptionThenEnvironmentThenDefault(t *testing.T) {
	t.Run("the default when nothing is set", func(t *testing.T) {
		m, port := readyMock(t)
		c, err := mongod.Start(context.Background(), mongod.WithDocker(m).WithPort(port))
		require.NoError(t, err, "a start against a fully scripted double must succeed")
		t.Cleanup(func() { _ = c.Stop(context.Background()) })

		assert.Equal(t, "mongo:8", c.Image(), "the in-code default is the Docker official image at the supported major version")
	})

	t.Run("the environment beats the default", func(t *testing.T) {
		t.Setenv("MONGOTEST_IMAGE", "mongo:7")
		m, port := readyMock(t)
		c, err := mongod.Start(context.Background(), mongod.WithDocker(m).WithPort(port))
		require.NoError(t, err, "a start against a fully scripted double must succeed")
		t.Cleanup(func() { _ = c.Stop(context.Background()) })

		assert.Equal(t, "mongo:7", c.Image(), "MONGOTEST_IMAGE is how a CI job pins an image for a whole suite without editing code")
	})

	t.Run("the option beats the environment", func(t *testing.T) {
		t.Setenv("MONGOTEST_IMAGE", "mongo:7")
		m, port := readyMock(t)
		c, err := mongod.Start(context.Background(), mongod.WithDocker(m).WithPort(port).WithImage("8.0.30"))
		require.NoError(t, err, "a start against a fully scripted double must succeed")
		t.Cleanup(func() { _ = c.Stop(context.Background()) })

		assert.Equal(t, "mongo:8.0.30", c.Image(), "a test that names an image needs that image, whatever the environment says")
	})
}

func TestEnvironmentVariablesForEachPart(t *testing.T) {
	t.Run("the parts override the whole", func(t *testing.T) {
		t.Setenv("MONGOTEST_IMAGE", "mongo:8")
		t.Setenv("MONGOTEST_IMAGE_REGISTRY", "public.ecr.aws")
		t.Setenv("MONGOTEST_IMAGE_REPOSITORY", "docker/library/mongo")
		m, port := readyMock(t)

		c, err := mongod.Start(context.Background(), mongod.WithDocker(m).WithPort(port))
		require.NoError(t, err, "a start against a fully scripted double must succeed")
		t.Cleanup(func() { _ = c.Stop(context.Background()) })

		assert.Equal(t, "public.ecr.aws/docker/library/mongo:8", c.Image(),
			"a CI job escaping Docker Hub's rate limits sets the registry alone and keeps the version it already pinned")
	})

	t.Run("the version alone", func(t *testing.T) {
		t.Setenv("MONGOTEST_IMAGE_VERSION", "8.0.30")
		m, port := readyMock(t)

		c, err := mongod.Start(context.Background(), mongod.WithDocker(m).WithPort(port))
		require.NoError(t, err, "a start against a fully scripted double must succeed")
		t.Cleanup(func() { _ = c.Stop(context.Background()) })

		assert.Equal(t, "mongo:8.0.30", c.Image(), "pinning just the version is the commonest CI override and must not require naming the repository too")
	})

	t.Run("the port", func(t *testing.T) {
		m, port := readyMock(t)
		t.Setenv("MONGOTEST_PORT", strconvItoa(port))

		c, err := mongod.Start(context.Background(), mongod.WithDocker(m))
		require.NoError(t, err, "a start against a fully scripted double must succeed")
		t.Cleanup(func() { _ = c.Stop(context.Background()) })

		assert.Equal(t, port, c.Port(), "MONGOTEST_PORT pins the host port for an environment that has to know it in advance")
	})

	t.Run("the start timeout", func(t *testing.T) {
		m, port := readyMock(t)
		t.Setenv("MONGOTEST_START_TIMEOUT", "90s")

		c, err := mongod.Start(context.Background(), mongod.WithDocker(m).WithPort(port))
		require.NoError(t, err, "a start against a fully scripted double must succeed")
		t.Cleanup(func() { _ = c.Stop(context.Background()) })

		assert.Equal(t, 90*time.Second, c.StartTimeout(), "a slow CI runner raises the budget for every test at once through the environment")
	})
}

func TestAnUnparseableEnvironmentValueIsReported(t *testing.T) {
	cases := map[string]string{
		"MONGOTEST_PORT":          "not-a-number",
		"MONGOTEST_START_TIMEOUT": "ages",
		"MONGOTEST_IMAGE":         "Mongo:8",
	}
	for name, value := range cases {
		t.Run(name, func(t *testing.T) {
			t.Setenv(name, value)
			m := &dockermock.Mock{}

			_, err := mongod.Start(context.Background(), mongod.WithDocker(m))

			require.Error(t, err, "a value that cannot be used is a misconfigured environment, and starting with the default instead would hide it")
			assert.ErrorIs(t, err, dockerclient.ErrInvalidArgument, "callers branch on the sentinel")
			assert.Contains(t, err.Error(), name, "the message has to name the variable at fault, because the reader has to go and change it")
			assert.Empty(t, m.CallsTo("ContainerCreate"), "nothing is created until the configuration is known to be usable")
		})
	}
}

func TestExplicitlySetZeroValuesSuppressTheEnvironment(t *testing.T) {
	t.Setenv("MONGOTEST_PORT", "27099")
	m, _ := readyMock(t)

	c, err := mongod.Start(context.Background(), mongod.WithDocker(m).WithPort(0))
	require.NoError(t, err, "a start against a fully scripted double must succeed")
	t.Cleanup(func() { _ = c.Stop(context.Background()) })

	assert.Equal(t, "", createdConfig(t, m).HostConfig.PortBindings["27017/tcp"][0].HostPort,
		"WithPort(0) is a caller saying 'let the daemon choose'; it is a decision and not an absence, so it beats the environment exactly as any other option does")
}

func TestOptionsReportTheirResolvedImage(t *testing.T) {
	t.Setenv("MONGOTEST_IMAGE_VERSION", "8.0.30")

	resolved, err := mongod.NewOptions().WithRepository("mongodb/mongodb-atlas-local").Resolve()

	require.NoError(t, err, "the resolution is pure string work and has no reason to fail here")
	assert.Equal(t, "mongodb/mongodb-atlas-local:8.0.30", resolved.Image.Reference(),
		"Resolve is exported so a caller can see what a set of options would actually start, which is the difference between debugging this and guessing")
}
