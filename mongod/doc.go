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
// With no options that runs mongo:8 on a host port the daemon chooses and
// waits up to a minute for mongod. The package imports no MongoDB driver:
// hand [Container.URI] to whichever one you use.
//
// # Options
//
// [Options] configures a start. Chain the With helpers, write a literal, or
// pass several to be merged left to right:
//
//	mongod.Start(ctx, mongod.WithImage("8.0").WithReplicaSet("rs0"))
//	mongod.Start(ctx, &mongod.Options{Image: mongod.ImageAtlasLocal})
//	mongod.Start(ctx, base, mongod.WithReplicaSet("rs0"))
//
// Each setting comes from the option, then an environment variable, then the
// default, so CI can configure a whole suite and one test can still override:
//
//	Setting        Option                      Environment                   Default
//	image          WithImage, WithMongoImage   MONGOTEST_IMAGE               mongo:8
//	  registry     WithRegistry                MONGOTEST_IMAGE_REGISTRY      Docker Hub
//	  repository   WithRepository              MONGOTEST_IMAGE_REPOSITORY    mongo
//	  version      WithVersion                 MONGOTEST_IMAGE_VERSION       8
//	host port      WithPort                    MONGOTEST_PORT                the daemon chooses
//	start timeout  WithStartTimeout            MONGOTEST_START_TIMEOUT       60s
//	dial address   WithHostIP                  MONGOTEST_HOST_IP             worked out
//
// Nothing is checked until [Options.Resolve], which Start calls. Call it
// yourself to see what a set of options means without starting anything.
//
// # Images
//
//	ImageDockerHub    mongo:8                                      the default
//	ImagePublicECR    public.ecr.aws/docker/library/mongo:8        no Docker Hub rate limit
//	ImageCommunity    mongodb/mongodb-community-server:8.0-ubi9    no bare version tags
//	ImageEnterprise   mongodb/mongodb-enterprise-server:8.0-ubi9   needs a licence
//	ImageAtlasLocal   mongodb/mongodb-atlas-local:8                Atlas Search; own replica set
//
// Two mistakes are refused up front, because the containers they produce
// fail without naming the cause: a bare version such as "8" for MongoDB's own
// builds ([ErrNoSuchVersion]), and WithReplicaSet or WithMongodArgs for Atlas
// Local ([ErrUnsupportedForImage]). An image this package does not know is
// treated like the official one.
//
// # When Start returns
//
// An image that creates a user or a replica set first runs a temporary
// mongod only it can reach, then replaces it. Start returns once the final
// mongod is running and its published port accepts a connection. That is not
// quite the same as mongod answering commands, so retry the first one, as a
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
// password. [Container.URI] carries the credentials either way, so a test
// that takes its URI from the container runs unchanged in both places.
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
