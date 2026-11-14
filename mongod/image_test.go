package mongod_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/tophergopher/mongotest/dockerclient"
	"github.com/tophergopher/mongotest/mongod"
)

func TestParseMongoImage(t *testing.T) {
	cases := []struct {
		ref  string
		want mongod.MongoImage
	}{
		{"8", mongod.MongoImage{Version: "8"}},
		{"8.0-ubi9", mongod.MongoImage{Version: "8.0-ubi9"}},
		{"latest", mongod.MongoImage{Version: "latest"}},
		{"mongo", mongod.MongoImage{Repository: "mongo"}},
		{"mongo:8.0", mongod.MongoImage{Repository: "mongo", Version: "8.0"}},
		{"mongodb/mongodb-atlas-local", mongod.MongoImage{Repository: "mongodb/mongodb-atlas-local"}},
		{"public.ecr.aws/docker/library/mongo", mongod.MongoImage{Registry: "public.ecr.aws", Repository: "docker/library/mongo"}},
		{"localhost:5000/team/mongo", mongod.MongoImage{Registry: "localhost:5000", Repository: "team/mongo"}},
		{"localhost:5000/team/mongo:8", mongod.MongoImage{Registry: "localhost:5000", Repository: "team/mongo", Version: "8"}},
		{"mongo@sha256:" + digest, mongod.MongoImage{Repository: "mongo", Version: "sha256:" + digest}},
		{"  mongo:8.0  ", mongod.MongoImage{Repository: "mongo", Version: "8.0"}},
	}
	for _, tc := range cases {
		t.Run(tc.ref, func(t *testing.T) {
			got, err := mongod.ParseMongoImage(tc.ref)

			require.NoError(t, err, "a reference docker accepts is parsed")
			assert.Equal(t, tc.want, got, "into the parts it names, leaving the rest empty")
		})
	}
}

const digest = "9f2b5c8e1d4a7f0b3c6e9d2a5f8b1c4e7d0a3f6b9c2e5d8a1f4b7c0e3d6a9f2b"

func TestParseMongoImageRefusesWhatDockerWould(t *testing.T) {
	for _, ref := range []string{"", "   ", "Mongo:8", "mongo:8:9", "8.0!"} {
		t.Run(ref, func(t *testing.T) {
			_, err := mongod.ParseMongoImage(ref)

			assert.ErrorIs(t, err, dockerclient.ErrInvalidArgument, "the mistake is caught before a daemon sees it")
		})
	}
}

func TestMustParseMongoImagePanicsOnAReferenceDockerWouldRefuse(t *testing.T) {
	assert.Panics(t, func() { mongod.MustParseMongoImage("Mongo:8") }, "it is for a reference written in the source, where an error has nowhere to go")
	assert.Equal(t, "mongo:8.0", mongod.MustParseMongoImage("mongo:8.0").Reference(), "and returns a good one")
}

func TestAnImageRendersAsDockerSpellsIt(t *testing.T) {
	cases := []struct {
		image mongod.MongoImage
		want  string
	}{
		{mongod.MongoImage{Repository: "mongo", Version: "8.0"}, "mongo:8.0"},
		{mongod.MongoImage{Registry: "public.ecr.aws", Repository: "docker/library/mongo", Version: "8.0"}, "public.ecr.aws/docker/library/mongo:8.0"},
		{mongod.MongoImage{Repository: "mongo"}, "mongo"},
		{mongod.MongoImage{Repository: "mongo", Version: "sha256:" + digest}, "mongo@sha256:" + digest},
		{mongod.MongoImage{Repository: "mongo", Version: "sha512:" + digest + digest}, "mongo@sha512:" + digest + digest},
	}
	for _, tc := range cases {
		assert.Equal(t, tc.want, tc.image.Reference(), "a tag is joined with a colon and a digest with @")
		assert.Equal(t, tc.want, tc.image.String(), "and an image prints as its reference")
	}
}

func TestDerivingFromAKnownImageLeavesItAlone(t *testing.T) {
	mirrored := mongod.ImageDockerHub.WithRegistry("mirror.corp.example").WithVersion("7.0")

	assert.Equal(t, "mirror.corp.example/mongo:7.0", mirrored.Reference(), "each With method returns a changed copy")
	assert.Equal(t, "mongo:8.0", mongod.ImageDockerHub.Reference(), "and the known image is unchanged for every other test")
}

// Whatever ParseMongoImage accepts must render as a reference that parses
// back to the same image.
func FuzzParseMongoImage(f *testing.F) {
	for _, seed := range []string{"8", "mongo", "mongo:8.0", "localhost:5000/team/mongo:8", "mongo@sha256:" + digest, "mongo@sha512:" + digest + digest} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, ref string) {
		parsed, err := mongod.ParseMongoImage(ref)
		if err != nil {
			return
		}
		again, err := mongod.ParseMongoImage(parsed.Reference())

		require.NoError(t, err, "%q rendered as %q, which does not parse", ref, parsed.Reference())
		assert.Equal(t, parsed, again, "%q changed when rendered and parsed again", ref)
	})
}
