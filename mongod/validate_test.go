package mongod_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/tophergopher/mongotest/dockerclient"
	"github.com/tophergopher/mongotest/dockermock"
	"github.com/tophergopher/mongotest/mongod"
)

// Validation runs after the options are built and before anything is created,
// so a combination that cannot work costs a message rather than a container
// and a timeout.

func TestABareVersionIsRejectedForAnImageThatPublishesNone(t *testing.T) {
	// Verified against the registry: mongodb/mongodb-community-server has no
	// tag "8", only "8.0-ubi9", "8.0.30-ubuntu2204" and the like. Applying a
	// bare version to it would fail at pull time with a 404 that says nothing
	// about why.
	cases := []struct {
		name  string
		image mongod.MongoImage
	}{
		{name: "community", image: mongod.ImageCommunity.WithVersion("8")},
		{name: "community with a minor version", image: mongod.ImageCommunity.WithVersion("8.0")},
		{name: "enterprise", image: mongod.ImageEnterprise.WithVersion("8.0.30")},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m := &dockermock.Mock{}

			_, err := mongod.Start(context.Background(), mongod.WithDocker(m).WithMongoImage(tc.image))

			require.Error(t, err, "this tag does not exist in that repository, and the registry's 404 would not say so")
			assert.ErrorIs(t, err, mongod.ErrNoSuchVersion, "callers branch on the sentinel")
			assert.ErrorIs(t, err, dockerclient.ErrInvalidArgument, "it is the argument that is wrong, not the daemon")
			assert.Contains(t, err.Error(), tc.image.Repository, "the message names the repository whose tags do not look like this")
			assert.Contains(t, err.Error(), "-ubi9", "the message has to show the shape that does work, or the reader is left guessing at the suffix")
			assert.Empty(t, m.CallsTo("ImagePull"), "the pull is not attempted, because it is known in advance that it cannot succeed")
		})
	}
}

func TestABareVersionIsFineForImagesThatPublishThem(t *testing.T) {
	// Also verified against the registry: the official image, the ECR mirror
	// of it and Atlas Local all publish bare tags.
	cases := []struct {
		name string
		img  mongod.MongoImage
		want string
	}{
		{name: "docker hub", img: mongod.ImageDockerHub.WithVersion("8.0"), want: "mongo:8.0"},
		{name: "public ecr", img: mongod.ImagePublicECR.WithVersion("8.0"), want: "public.ecr.aws/docker/library/mongo:8.0"},
		{name: "atlas local", img: mongod.ImageAtlasLocal.WithVersion("8.0"), want: "mongodb/mongodb-atlas-local:8.0"},
		{name: "an unknown repository", img: mongod.NewMongoImage("registry.example.test", "team/mongo", "8"), want: "registry.example.test/team/mongo:8"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			resolved, err := mongod.NewOptions().WithMongoImage(tc.img).Resolve()

			require.NoError(t, err, "this repository does publish bare version tags, so there is nothing to object to")
			assert.Equal(t, tc.want, resolved.Image.Reference(), "the reference is passed through unchanged")
		})
	}
}

func TestAtlasLocalRejectsOptionsThatWouldReplaceItsEntrypoint(t *testing.T) {
	// Verified against the image: mongodb/mongodb-atlas-local has no
	// entrypoint and its Cmd is ["/usr/local/bin/runner server"], so a Cmd of
	// mongod flags replaces the program itself. The container then dies with
	// exec: "--replSet": executable file not found in $PATH, exit code 127,
	// which explains nothing about the option that caused it.
	cases := []struct {
		name    string
		options *mongod.Options
		names   string
	}{
		{name: "a replica set", options: mongod.WithReplicaSet("rs0"), names: "WithReplicaSet"},
		{name: "mongod arguments", options: mongod.WithMongodArgs("--setParameter", "enableTestCommands=1"), names: "WithMongodArgs"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m := &dockermock.Mock{}

			_, err := mongod.Start(context.Background(), tc.options, mongod.WithDocker(m).WithMongoImage(mongod.ImageAtlasLocal))

			require.Error(t, err, "this combination produces a container that cannot exec at all, so it is refused rather than attempted")
			assert.ErrorIs(t, err, mongod.ErrUnsupportedForImage, "callers branch on the sentinel")
			assert.Contains(t, err.Error(), tc.names, "the message names the option to remove")
			assert.Contains(t, err.Error(), mongod.ImageAtlasLocal.Repository, "the message names the image that does not accept it")
			assert.Empty(t, m.CallsTo("ContainerCreate"), "nothing is created for a configuration known to be unstartable")
		})
	}
}

func TestAtlasLocalExplainsThatItAlreadyRunsAReplicaSet(t *testing.T) {
	m := &dockermock.Mock{}

	_, err := mongod.Start(context.Background(), mongod.WithDocker(m).WithMongoImage(mongod.ImageAtlasLocal).WithReplicaSet("rs0"))

	require.Error(t, err, "asking Atlas Local for a replica set is both broken and unnecessary")
	assert.Contains(t, err.Error(), "already", "the reader needs to be told the option is redundant, not just forbidden, or they will go looking for another way to get one")
}

func TestAtlasLocalStartsWithNoFlags(t *testing.T) {
	m, port := readyMock(t)

	c, err := mongod.Start(context.Background(), mongod.WithDocker(m).WithPort(port).WithMongoImage(mongod.ImageAtlasLocal))
	require.NoError(t, err, "Atlas Local needs nothing beyond the image itself")
	t.Cleanup(func() { _ = c.Stop(context.Background()) })

	assert.Empty(t, createdConfig(t, m).Cmd, "the image's own command is what runs it, so the create request must leave Cmd alone")
	assert.Equal(t, "mongodb/mongodb-atlas-local:8", c.Image(), "the image is the one that was asked for")
}

func TestReplicaSetPreconfiguredIsReportedForAtlasLocal(t *testing.T) {
	assert.True(t, mongod.ImageAtlasLocal.ReplicaSetPreconfigured(),
		"Atlas Local brings up a single-node replica set of its own, under a name it generates, so a driver layer has to read the name rather than assume one")
	assert.False(t, mongod.ImageDockerHub.ReplicaSetPreconfigured(),
		"the official image is a standalone server until --replSet says otherwise")
	assert.False(t, mongod.NewMongoImage("registry.example.test", "team/mongo", "8").ReplicaSetPreconfigured(),
		"an image this package knows nothing about is assumed to behave like the official one, which is the permissive answer and the right default for a private mirror")
}

func TestAcceptsMongodArgsIsReportedPerImage(t *testing.T) {
	assert.True(t, mongod.ImageDockerHub.AcceptsMongodArgs(), "the official image's entrypoint passes its arguments to mongod")
	assert.True(t, mongod.ImageCommunity.AcceptsMongodArgs(), "MongoDB's community build does the same through its python entrypoint")
	assert.False(t, mongod.ImageAtlasLocal.AcceptsMongodArgs(), "Atlas Local's Cmd is its entrypoint, so arguments replace the program rather than reaching mongod")
	assert.True(t, mongod.NewMongoImage("registry.example.test", "team/mongo", "8").AcceptsMongodArgs(),
		"an unknown image is assumed to behave like the official one rather than being refused options it may well accept")
}

func TestAnEmptyRepositoryIsRejected(t *testing.T) {
	m := &dockermock.Mock{}

	_, err := mongod.Start(context.Background(), mongod.WithDocker(m).WithRepository(""))

	require.Error(t, err, "an explicitly emptied repository names nothing, and falling back to the default would ignore what the caller asked for")
	assert.ErrorIs(t, err, dockerclient.ErrInvalidArgument, "callers branch on the sentinel")
}

func TestResolveIsPureAndNeedsNoDaemon(t *testing.T) {
	// Resolution is string work. It has to be callable without a client at
	// all, so that a caller can check what their options mean in a unit test.
	resolved, err := mongod.WithImage("public.ecr.aws/docker/library/mongo:8.0").WithPort(27018).Resolve()

	require.NoError(t, err, "no daemon is needed to work out what a set of options means")
	assert.Equal(t, "public.ecr.aws/docker/library/mongo:8.0", resolved.Image.Reference(), "the image is resolved")
	assert.Equal(t, 27018, resolved.Port, "the port is resolved")
	assert.Equal(t, "regression", resolved.Labels["mongotest"], "the required label is present after resolution, which is what the integration suite filters on")
	assert.NotEmpty(t, resolved.Name, "a name is generated during resolution, so that what gets created is knowable in advance")
}
