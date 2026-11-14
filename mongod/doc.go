// Package mongod starts and stops MongoDB containers for tests.
//
//	c, err := mongod.Start(ctx)
//	if err != nil {
//		return err
//	}
//	defer c.Stop(context.Background())
//
//	client, err := mongo.Connect(options.Client().ApplyURI(c.URI()))
//
// With no options that runs mongo:8.0 on a host port the daemon chooses and
// waits up to a minute for mongod. The package imports no MongoDB driver:
// hand [Container.URI] to whichever one you use.
//
// # Options
//
// Options change a start, and apply in the order given:
//
//	mongod.Start(ctx, mongod.WithImage("7.0"), mongod.WithReplicaSet("rs0"))
//
// Three settings can also come from the environment, so CI can configure a
// whole suite. An option overrides the variable:
//
//	MONGOTEST_IMAGE           the image, in any shape WithImage accepts
//	MONGOTEST_START_TIMEOUT   how long to wait for mongod, such as "90s"
//	MONGOTEST_HOST_IP         the address containers are reachable at
//
// # Images
//
//	ImageDockerHub    mongo:8.0                                    the default
//	ImagePublicECR    public.ecr.aws/docker/library/mongo:8.0      no Docker Hub rate limit
//	ImageCommunity    mongodb/mongodb-community-server:8.0-ubi9    no bare version tags
//	ImageEnterprise   mongodb/mongodb-enterprise-server:8.0-ubi9   needs a licence
//	ImageAtlasLocal   mongodb/mongodb-atlas-local:8.0              Atlas Search; own replica set
//
// Two mistakes are refused up front, because the containers they produce
// fail without naming the cause: a bare version such as "8.0" for
// ImageCommunity or ImageEnterprise ([ErrNoSuchVersion]), and WithReplicaSet
// or WithMongodArgs for Atlas Local ([ErrUnsupportedForImage]). An image this
// package does not know is treated like the official one.
//
// # When Start returns
//
// An image that creates a user or a replica set first runs a temporary
// mongod only it can reach, then replaces it. Start returns once the final
// mongod is running and its published port accepts a connection. That does
// not prove mongod answers commands yet, so retry the first command, as a
// driver's ping does.
//
// # Where to connect
//
// The published port is on the machine the daemon runs on, which is not
// always this one. Start works the address out: a remote daemon's host,
// loopback for a local one, or the gateway when this process is itself in a
// container. [WithHostIP] lists the order, and overrides it for a network
// the detection gets wrong.
//
// # Who can connect
//
// A local container is published on 127.0.0.1 only, and mongod runs without
// authentication. Any other address needs the port published on every
// interface, so Start creates a root user, "mongotest", with a random
// password. [Container.URI] includes the credentials when there are any, so
// a test that takes its URI from the container runs unchanged in both places.
//
//	mongod.Start(ctx, mongod.WithAuth("admin", "secret"))   // always these
//	mongod.Start(ctx, mongod.WithoutAuth())                 // never any
//
// Creating the user makes a start a few seconds slower. Anyone who can use
// the Docker daemon can read the credentials, and traffic is not encrypted.
//
// # Cleaning up
//
// Stop removes the container. A container that is never stopped is still
// removed if the process gets SIGINT or SIGTERM; see the reaper package.
// [ReapRunningContainers] does the same on demand: call it after m.Run in
// TestMain. SIGKILL cannot be handled, so containers outlive it. Each is
// labelled so it can be found:
//
//	docker rm -f $(docker ps -aq --filter label=mongotest=regression)
package mongod
