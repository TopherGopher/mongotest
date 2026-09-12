package mongod_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/tophergopher/mongotest/dockerclient"
	"github.com/tophergopher/mongotest/mongod"
)

func TestNewMongoImageTakesTheThreeParts(t *testing.T) {
	img := mongod.NewMongoImage("123456789012.dkr.ecr.eu-west-1.amazonaws.com", "platform/mongo", "8.0-hardened")

	assert.Equal(t, "123456789012.dkr.ecr.eu-west-1.amazonaws.com", img.Registry, "the registry is the host the image is pulled from")
	assert.Equal(t, "platform/mongo", img.Repository, "the repository is the path within that registry")
	assert.Equal(t, "8.0-hardened", img.Version, "the version is the tag")
	assert.Equal(t, "123456789012.dkr.ecr.eu-west-1.amazonaws.com/platform/mongo:8.0-hardened",
		img.Reference(), "the reference is what goes to the daemon, and it is the three parts joined the way docker spells them")
}

func TestMongoImageReferenceOmitsAnEmptyRegistry(t *testing.T) {
	img := mongod.NewMongoImage("", "mongo", "8")

	assert.Equal(t, "mongo:8", img.Reference(),
		"an empty registry means Docker Hub, and spelling it out as docker.io/library/mongo would be a different string for the same image in every log line and test assertion")
}

func TestMongoImageReferenceUsesAtForADigest(t *testing.T) {
	const digest = "sha256:9f2b5c8e7a1d4f6b3c0e8d2a5f7b9c1e4d6a8f0b2c5e7d9a1f3b5c7e9d1a3f5b"
	img := mongod.NewMongoImage("", "mongo", digest)

	assert.Equal(t, "mongo@"+digest, img.Reference(),
		"a digest is separated by @ and not by :, so pinning by digest has to produce a reference the daemon accepts")
}

func TestMongoImagePartsAreReplaceableWithoutTouchingTheOthers(t *testing.T) {
	base := mongod.ImageCommunity

	derived := base.WithVersion("8.0-ubi8")

	assert.Equal(t, "8.0-ubi8", derived.Version, "WithVersion is how a caller takes a known image at a version of their choosing")
	assert.Equal(t, base.Repository, derived.Repository, "changing the version must not disturb the repository")
	assert.Equal(t, "8.0-ubi9", mongod.ImageCommunity.Version,
		"the well-known images are shared package state, so deriving from one must copy it rather than modify it")

	mirrored := base.WithRegistry("registry.example.test").WithRepository("mirror/mongodb-community-server")
	assert.Equal(t, "registry.example.test/mirror/mongodb-community-server:8.0-ubi9",
		mirrored.Reference(), "the parts are independently replaceable, which is what makes a private mirror of a known image one call away")
}

func TestWellKnownImages(t *testing.T) {
	cases := []struct {
		name string
		img  mongod.MongoImage
		want string
		why  string
	}{
		{
			name: "docker hub official", img: mongod.ImageDockerHub, want: "mongo:8",
			why: "the Docker official image is the default, and it publishes a bare major tag",
		},
		{
			name: "public ecr mirror", img: mongod.ImagePublicECR, want: "public.ecr.aws/docker/library/mongo:8",
			why: "the ECR Public mirror of the official image is the way out of Docker Hub's pull rate limits, and it is the same image under a different path",
		},
		{
			name: "mongodb community server", img: mongod.ImageCommunity, want: "mongodb/mongodb-community-server:8.0-ubi9",
			why: "MongoDB's own community build publishes no bare major tag, so the default has to name an OS variant or it does not resolve",
		},
		{
			name: "mongodb enterprise server", img: mongod.ImageEnterprise, want: "mongodb/mongodb-enterprise-server:8.0-ubi9",
			why: "the enterprise build is tagged the same way as the community one",
		},
		{
			name: "atlas local", img: mongod.ImageAtlasLocal, want: "mongodb/mongodb-atlas-local:8",
			why: "Atlas Local is how a test gets Atlas Search locally, and unlike the other MongoDB-published images it does publish bare tags",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, tc.img.Reference(), tc.why)
			_, err := dockerclient.ParseImageRef(tc.img.Reference())
			assert.NoError(t, err, "a well-known image has to be a reference the client will actually accept, not just a plausible string")
		})
	}
}

func TestParseMongoImageAcceptsEveryShapeACallerMightPass(t *testing.T) {
	cases := []struct {
		name  string
		input string
		want  mongod.MongoImage
		why   string
	}{
		{
			name: "a bare major version", input: "8",
			want: mongod.MongoImage{Version: "8"},
			why:  "a version on its own is the commonest thing to want to change, and it says nothing about where the image comes from",
		},
		{
			name: "a minor version", input: "8.0",
			want: mongod.MongoImage{Version: "8.0"},
			why:  "anything starting with a digit is a version rather than a repository",
		},
		{
			name: "a patch version", input: "8.0.30",
			want: mongod.MongoImage{Version: "8.0.30"},
			why:  "a full semver tag is still just a version",
		},
		{
			name: "a version with an os variant", input: "8.0-ubi9",
			want: mongod.MongoImage{Version: "8.0-ubi9"},
			why:  "MongoDB's own images need the OS suffix, and it is part of the tag and not a repository",
		},
		{
			name: "the word latest", input: "latest",
			want: mongod.MongoImage{Version: "latest"},
			why:  "latest is a tag, not a repository, even though it starts with a letter",
		},
		{
			name: "a repository on its own", input: "mongo",
			want: mongod.MongoImage{Repository: "mongo"},
			why:  "a bare word that is not a version names a repository, and the version is filled in later",
		},
		{
			name: "an organisation and repository", input: "mongodb/mongodb-atlas-local",
			want: mongod.MongoImage{Repository: "mongodb/mongodb-atlas-local"},
			why:  "a single slash with no dot in the first element is a Docker Hub organisation, not a registry host",
		},
		{
			name: "repository and tag", input: "mongo:8.0",
			want: mongod.MongoImage{Repository: "mongo", Version: "8.0"},
			why:  "the form nearly every docker command is written in",
		},
		{
			name: "a registry and repository with no tag", input: "public.ecr.aws/docker/library/mongo",
			want: mongod.MongoImage{Registry: "public.ecr.aws", Repository: "docker/library/mongo"},
			why:  "a dot in the first element makes it a registry host, so the rest is the repository path",
		},
		{
			name: "a full private ECR path with a tag", input: "123456789012.dkr.ecr.us-east-1.amazonaws.com/platform/mongo:8.0-hardened",
			want: mongod.MongoImage{Registry: "123456789012.dkr.ecr.us-east-1.amazonaws.com", Repository: "platform/mongo", Version: "8.0-hardened"},
			why:  "this is the shape a team with its own hardened build passes, and all three parts have to come out separately",
		},
		{
			name: "a registry with a port", input: "localhost:5000/team/mongo:8",
			want: mongod.MongoImage{Registry: "localhost:5000", Repository: "team/mongo", Version: "8"},
			why:  "a colon in the first element is a registry port, not a tag; mistaking the two is the classic parser bug",
		},
		{
			name: "localhost with no port", input: "localhost/mongo:8",
			want: mongod.MongoImage{Registry: "localhost", Repository: "mongo", Version: "8"},
			why:  "localhost is a registry host by name even without a dot or a port, which is the one exception to the dot rule",
		},
		{
			name: "a digest", input: "mongo@sha256:9f2b5c8e7a1d4f6b3c0e8d2a5f7b9c1e4d6a8f0b2c5e7d9a1f3b5c7e9d1a3f5b",
			want: mongod.MongoImage{Repository: "mongo", Version: "sha256:9f2b5c8e7a1d4f6b3c0e8d2a5f7b9c1e4d6a8f0b2c5e7d9a1f3b5c7e9d1a3f5b"},
			why:  "pinning by digest is how a test is made reproducible, and the digest takes the version's place",
		},
		{
			name: "surrounding space", input: "  mongo:8  ",
			want: mongod.MongoImage{Repository: "mongo", Version: "8"},
			why:  "the value often comes from an environment variable or a CI template, where a stray space is common and harmless",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := mongod.ParseMongoImage(tc.input)

			require.NoError(t, err, "this shape is one a caller is expected to pass, so it must parse")
			assert.Equal(t, tc.want.Registry, got.Registry, "registry: "+tc.why)
			assert.Equal(t, tc.want.Repository, got.Repository, "repository: "+tc.why)
			assert.Equal(t, tc.want.Version, got.Version, "version: "+tc.why)
		})
	}
}

func TestParseMongoImageRejectsWhatTheDaemonWould(t *testing.T) {
	cases := []struct {
		name  string
		input string
		why   string
	}{
		{name: "empty", input: "", why: "an empty value names nothing, and silently substituting the default would hide a misconfigured environment variable"},
		{name: "only a colon", input: ":", why: "there is neither a repository nor a tag here"},
		{name: "an uppercase repository", input: "Mongo:8", why: "docker requires lowercase repositories, and the daemon would answer with a confusing 400"},
		{name: "a tag with a space", input: "mongo:8 0", why: "a space cannot appear in a tag, and the usual cause is a quoting mistake in CI"},
		{name: "two tags", input: "mongo:8:9", why: "this is ambiguous, and guessing which colon was meant would pull something the caller did not ask for"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := mongod.ParseMongoImage(tc.input)

			require.Error(t, err, tc.why)
			assert.ErrorIs(t, err, dockerclient.ErrInvalidArgument, "a reference the daemon would refuse is a client-side argument problem, which callers branch on with errors.Is")
		})
	}
}
