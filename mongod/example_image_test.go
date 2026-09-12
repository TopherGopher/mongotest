package mongod_test

import (
	"context"
	"fmt"

	"github.com/tophergopher/mongotest/mongod"
)

func ExampleNewMongoImage() {
	// The three parts a caller thinks in: where it comes from, what it is
	// called there, and which version.
	img := mongod.NewMongoImage(
		"123456789012.dkr.ecr.eu-west-1.amazonaws.com",
		"platform/mongo",
		"8.0-hardened",
	)

	fmt.Println(img)
	// Output: 123456789012.dkr.ecr.eu-west-1.amazonaws.com/platform/mongo:8.0-hardened
}

func ExampleMongoImage() {
	// The images this package knows about. An empty registry means Docker
	// Hub, so the reference stays the short form everyone recognises.
	for _, img := range []mongod.MongoImage{
		mongod.ImageDockerHub,
		mongod.ImagePublicECR,
		mongod.ImageCommunity,
		mongod.ImageEnterprise,
		mongod.ImageAtlasLocal,
	} {
		fmt.Println(img)
	}
	// Output:
	// mongo:8
	// public.ecr.aws/docker/library/mongo:8
	// mongodb/mongodb-community-server:8.0-ubi9
	// mongodb/mongodb-enterprise-server:8.0-ubi9
	// mongodb/mongodb-atlas-local:8
}

func ExampleMongoImage_WithVersion() {
	// Take a known image at a version of your choosing. The original is
	// unchanged, so the package-level images stay what they are.
	fmt.Println(mongod.ImageCommunity.WithVersion("8.0-ubi8"))
	fmt.Println(mongod.ImageCommunity)
	// Output:
	// mongodb/mongodb-community-server:8.0-ubi8
	// mongodb/mongodb-community-server:8.0-ubi9
}

func ExampleMongoImage_WithRegistry() {
	// A known image from a private mirror: keep the repository and version,
	// change only where it is pulled from.
	fmt.Println(mongod.ImageDockerHub.WithRegistry("mirror.corp.example"))
	// Output: mirror.corp.example/mongo:8
}

func ExampleMongoImage_WithRepository() {
	fmt.Println(mongod.ImagePublicECR.WithRepository("my-team/mongo-patched"))
	// Output: public.ecr.aws/my-team/mongo-patched:8
}

func ExampleMongoImage_Reference() {
	// A digest is joined with @ rather than :, which is what pins a test to
	// exactly the same bytes on every machine.
	pinned := mongod.ImageDockerHub.WithVersion("sha256:9f2b5c8e7a1d4f6b3c0e8d2a5f7b9c1e4d6a8f0b2c5e7d9a1f3b5c7e9d1a3f5b")

	fmt.Println(pinned.Reference())
	// Output: mongo@sha256:9f2b5c8e7a1d4f6b3c0e8d2a5f7b9c1e4d6a8f0b2c5e7d9a1f3b5c7e9d1a3f5b
}

func ExampleMongoImage_PinnedByDigest() {
	fmt.Println(mongod.ImageDockerHub.PinnedByDigest())
	fmt.Println(mongod.ImageDockerHub.WithVersion("sha256:9f2b5c8e7a1d4f6b3c0e8d2a5f7b9c1e4d6a8f0b2c5e7d9a1f3b5c7e9d1a3f5b").PinnedByDigest())
	// Output:
	// false
	// true
}

func ExampleMongoImage_BareVersionTags() {
	// MongoDB's own server images publish no bare version tags: every tag
	// names an OS variant. Start refuses a bare version for them rather than
	// letting the pull fail with a 404 that does not say why.
	fmt.Println("docker hub:", mongod.ImageDockerHub.BareVersionTags())
	fmt.Println("community: ", mongod.ImageCommunity.BareVersionTags())
	// Output:
	// docker hub: true
	// community:  false
}

func ExampleMongoImage_AcceptsMongodArgs() {
	// Atlas Local's command is its entrypoint, so flags given as a command
	// replace the program instead of reaching mongod.
	fmt.Println("docker hub: ", mongod.ImageDockerHub.AcceptsMongodArgs())
	fmt.Println("atlas local:", mongod.ImageAtlasLocal.AcceptsMongodArgs())
	// Output:
	// docker hub:  true
	// atlas local: false
}

func ExampleMongoImage_ReplicaSetPreconfigured() {
	// Atlas Local brings up a single-node replica set of its own, under a name
	// it generates, so a driver layer reads the name rather than choosing it.
	fmt.Println("docker hub: ", mongod.ImageDockerHub.ReplicaSetPreconfigured())
	fmt.Println("atlas local:", mongod.ImageAtlasLocal.ReplicaSetPreconfigured())
	// Output:
	// docker hub:  false
	// atlas local: true
}

func ExampleMongoImage_DataDir() {
	fmt.Println(mongod.ImageDockerHub.DataDir())
	// Output: /data/db
}

func ExampleMongoImage_IsZero() {
	fmt.Println(mongod.MongoImage{}.IsZero())
	fmt.Println(mongod.ImageDockerHub.IsZero())
	// Output:
	// true
	// false
}

func ExampleMongoImage_String() {
	// String is Reference, so an image can be printed or logged directly.
	fmt.Printf("pulling %s\n", mongod.ImageAtlasLocal)
	// Output: pulling mongodb/mongodb-atlas-local:8
}

func ExampleParseMongoImage() {
	// Every shape a caller is likely to have an image in. Whatever a shape
	// does not name is filled in during resolution, so a version on its own
	// keeps the default repository and a repository on its own gets that
	// repository's own default version.
	for _, ref := range []string{
		"8.0",
		"latest",
		"mongo",
		"mongo:8.0",
		"mongodb/mongodb-atlas-local",
		"public.ecr.aws/docker/library/mongo",
		"123456789012.dkr.ecr.us-east-1.amazonaws.com/platform/mongo:8.0-hardened",
		"localhost:5000/team/mongo:8",
	} {
		img, err := mongod.ParseMongoImage(ref)
		if err != nil {
			fmt.Println("cannot parse:", err)
			continue
		}
		fmt.Printf("%q -> registry=%q repository=%q version=%q\n", ref, img.Registry, img.Repository, img.Version)
	}
	// Output:
	// "8.0" -> registry="" repository="" version="8.0"
	// "latest" -> registry="" repository="" version="latest"
	// "mongo" -> registry="" repository="mongo" version=""
	// "mongo:8.0" -> registry="" repository="mongo" version="8.0"
	// "mongodb/mongodb-atlas-local" -> registry="" repository="mongodb/mongodb-atlas-local" version=""
	// "public.ecr.aws/docker/library/mongo" -> registry="public.ecr.aws" repository="docker/library/mongo" version=""
	// "123456789012.dkr.ecr.us-east-1.amazonaws.com/platform/mongo:8.0-hardened" -> registry="123456789012.dkr.ecr.us-east-1.amazonaws.com" repository="platform/mongo" version="8.0-hardened"
	// "localhost:5000/team/mongo:8" -> registry="localhost:5000" repository="team/mongo" version="8"
}

func ExampleNewOptions() {
	docker, port, cleanup := exampleDaemon()
	defer cleanup()

	// NewOptions starts a chain explicitly, which reads better than a
	// package-level helper when there are several settings.
	opts := mongod.NewOptions().
		WithDocker(docker).
		WithPort(port).
		WithMongoImage(mongod.ImageCommunity.WithVersion("8.0-ubi8")).
		WithReplicaSet("rs0")

	c, err := mongod.Start(context.Background(), opts)
	if err != nil {
		fmt.Println("cannot start mongod:", err)
		return
	}
	defer func() { _ = c.Stop(context.Background()) }()

	fmt.Println(c.Image(), c.ReplicaSet())
	// Output: mongodb/mongodb-community-server:8.0-ubi8 rs0
}

func ExampleOptions() {
	docker, port, cleanup := exampleDaemon()
	defer cleanup()

	// The fields are public, which is what a table-driven test usually wants.
	c, err := mongod.Start(context.Background(), &mongod.Options{
		Docker: docker,
		Port:   port,
		Image:  mongod.ImageDockerHub.WithVersion("8.0.30"),
		Labels: map[string]string{"suite": "checkout"},
	})
	if err != nil {
		fmt.Println("cannot start mongod:", err)
		return
	}
	defer func() { _ = c.Stop(context.Background()) }()

	fmt.Println(c.Image())
	// Output: mongo:8.0.30
}

func ExampleOptions_Resolve() {
	// Resolve answers every question without starting anything or talking to a
	// daemon, so a caller can see what a configuration actually means.
	resolved, err := mongod.WithImage("8.0").WithRegistry(mongod.RegistryPublicECR).
		WithRepository(mongod.RepositoryPublicECRMirror).Resolve()
	if err != nil {
		fmt.Println("cannot resolve:", err)
		return
	}

	fmt.Println("image:   ", resolved.Image)
	fmt.Println("port:    ", resolved.Port, "(0 means the daemon chooses)")
	fmt.Println("timeout: ", resolved.StartTimeout)
	fmt.Println("label:   ", resolved.Labels[mongod.RequiredLabelKey])
	// Output:
	// image:    public.ecr.aws/docker/library/mongo:8.0
	// port:     0 (0 means the daemon chooses)
	// timeout:  1m0s
	// label:    regression
}

func ExampleOptions_Resolve_rejectsWhatCannotWork() {
	// Atlas Local already runs a replica set, and its command is its
	// entrypoint, so asking for one would produce a container that cannot
	// exec at all. That is caught before anything is created.
	_, err := mongod.WithMongoImage(mongod.ImageAtlasLocal).WithReplicaSet("rs0").Resolve()
	fmt.Println(err)

	// A bare version does not exist in MongoDB's own repositories.
	_, err = mongod.WithMongoImage(mongod.ImageCommunity.WithVersion("8")).Resolve()
	fmt.Println(err)
	// Output:
	// mongod: mongodb/mongodb-atlas-local does not support WithReplicaSet: it already runs a single-node replica set of its own, under a name it generates from the container's hostname, so there is nothing to ask for and the name cannot be chosen; read it from the server instead
	// mongod: mongodb/mongodb-community-server publishes no tag "8": every tag in that repository names an OS variant, so a bare version does not exist there. Pass a tag such as "8.0-ubi9", or use one of the images that does publish bare versions (mongod.ImageDockerHub, mongod.ImagePublicECR, mongod.ImageAtlasLocal)
}

func ExampleWithMongoImage() {
	docker, port, cleanup := exampleDaemon()
	defer cleanup()

	c, err := mongod.Start(context.Background(),
		mongod.WithMongoImage(mongod.ImageAtlasLocal).WithDocker(docker).WithPort(port))
	if err != nil {
		fmt.Println("cannot start mongod:", err)
		return
	}
	defer func() { _ = c.Stop(context.Background()) }()

	fmt.Println(c.Image())
	// Output: mongodb/mongodb-atlas-local:8
}

func ExampleWithRegistry() {
	// Moving a whole suite off Docker Hub, to get out from under its pull
	// rate limits, without touching the version the tests pin.
	resolved, err := mongod.WithRegistry(mongod.RegistryPublicECR).
		WithRepository(mongod.RepositoryPublicECRMirror).Resolve()
	if err != nil {
		fmt.Println("cannot resolve:", err)
		return
	}

	fmt.Println(resolved.Image)
	// Output: public.ecr.aws/docker/library/mongo:8
}

func ExampleWithRepository() {
	resolved, err := mongod.WithRepository(mongod.RepositoryCommunity).Resolve()
	if err != nil {
		fmt.Println("cannot resolve:", err)
		return
	}

	// The version defaults per repository, because MongoDB's own images have
	// no bare major tag to fall back on.
	fmt.Println(resolved.Image)
	// Output: mongodb/mongodb-community-server:8.0-ubi9
}

func ExampleWithVersion() {
	resolved, err := mongod.WithVersion("8.0.30").Resolve()
	if err != nil {
		fmt.Println("cannot resolve:", err)
		return
	}

	fmt.Println(resolved.Image)
	// Output: mongo:8.0.30
}

func ExampleContainer_MongoImage() {
	docker, port, cleanup := exampleDaemon()
	defer cleanup()
	c, cleanupContainer := exampleContainer(docker, port)
	defer cleanupContainer()

	img := c.MongoImage()
	fmt.Printf("registry=%q repository=%q version=%q\n", img.Registry, img.Repository, img.Version)
	// Output: registry="" repository="mongo" version="8"
}

func ExampleContainer_StartTimeout() {
	docker, port, cleanup := exampleDaemon()
	defer cleanup()
	c, cleanupContainer := exampleContainer(docker, port)
	defer cleanupContainer()

	// What the option, the environment and the default worked out to.
	fmt.Println(c.StartTimeout())
	// Output: 1m0s
}
