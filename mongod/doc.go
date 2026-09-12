// Package mongod starts and stops MongoDB containers.
//
// It knows nothing about the MongoDB wire protocol and imports no driver,
// which is what lets the driver v1 root module and the v2 module share it.
// What it offers is a container that is running, published at an address this
// process can actually reach, and with mongod itself started; connecting to it
// is the caller's job.
//
// # Starting one
//
//	c, err := mongod.Start(ctx)
//	if err != nil {
//		return err
//	}
//	defer c.Stop(context.Background())
//
//	client, err := mongo.Connect(options.Client().ApplyURI(c.URI()))
//
// With no options that runs mongo:8, publishes 27017 on a host port the
// daemon chooses, labels the container mongotest=regression and waits up to a
// minute for mongod to answer.
//
// # Configuring one
//
// Options carries everything, and can be built in whichever style suits the
// call site. Chained, which is the usual form:
//
//	c, err := mongod.Start(ctx, mongod.WithImage("8.0").WithReplicaSet("rs0"))
//
// Or written out, which is what a table-driven test usually wants, since the
// fields are public:
//
//	c, err := mongod.Start(ctx, &mongod.Options{Image: mongod.ImageAtlasLocal})
//
// Several are merged left to right, so a shared base can be specialised per
// case without being modified:
//
//	base := mongod.NewOptions().WithDocker(client).WithImage("8.0")
//	c, err := mongod.Start(ctx, base, mongod.WithReplicaSet("rs0"))
//
// # Where each setting comes from
//
// Every setting is taken from the first of three places that has it: the
// option, then the environment variable, then the built-in default. The
// environment is how a CI job configures a whole suite without editing code;
// the option is how one test overrides it.
//
//	Setting          Option                   Environment                   Default
//	image            WithImage, WithMongoImage MONGOTEST_IMAGE               mongo:8
//	  registry       WithRegistry             MONGOTEST_IMAGE_REGISTRY      Docker Hub
//	  repository     WithRepository           MONGOTEST_IMAGE_REPOSITORY    mongo
//	  version        WithVersion              MONGOTEST_IMAGE_VERSION       8
//	host port        WithPort                 MONGOTEST_PORT                the daemon chooses
//	readiness budget WithStartTimeout         MONGOTEST_START_TIMEOUT       60s
//	dial address     WithHostIP               MONGOTEST_HOST_IP             worked out; see Endpoint
//
// The image parts are independent, in both the option and the environment. A
// CI job escaping Docker Hub's pull rate limits sets the registry and
// repository and keeps whatever version its tests already pin.
//
// Inference and validation happen once, in [Options.Resolve], which Start
// calls. No helper returns an error: a bad value is kept and reported from
// there, so a chain stays one expression and nothing is created on a
// configuration that cannot work. Call Resolve yourself to see what a set of
// options means:
//
//	resolved, err := mongod.WithImage("8.0").Resolve()
//	fmt.Println(resolved.Image) // mongo:8.0
//
// WithImage splits its reference as it is called, so [Options.Image] holds the
// registry, repository and version straight away and can be read or asserted
// on without resolving anything. It sets only the parts the reference names,
// so a bare version behaves exactly like WithVersion.
//
// # Which image
//
// [MongoImage] holds an image in the three parts a caller thinks in, so one
// part can be changed without restating the others. The images this package
// knows about, and how they differ:
//
//	Constant          Reference                                   Notes
//	ImageDockerHub    mongo:8                                     the default
//	ImagePublicECR    public.ecr.aws/docker/library/mongo:8        the same image, no Docker Hub rate limit
//	ImageCommunity    mongodb/mongodb-community-server:8.0-ubi9    MongoDB's own build; no bare version tags
//	ImageEnterprise   mongodb/mongodb-enterprise-server:8.0-ubi9   needs a licence beyond evaluation
//	ImageAtlasLocal   mongodb/mongodb-atlas-local:8                adds Atlas Search; runs its own replica set
//
// Take one and change what you need, or build one from its parts:
//
//	mongod.ImageCommunity.WithVersion("8.0-ubi8")
//	mongod.ImageDockerHub.WithRegistry("mirror.corp.example")
//	mongod.NewMongoImage("1234.dkr.ecr.eu-west-1.amazonaws.com", "platform/mongo", "8.0-hardened")
//
// WithImage takes a reference in whatever shape you have one, and
// [ParseMongoImage] documents the shapes: a version on its own ("8.0"), a
// repository ("mongo"), both ("mongo:8.0"), a full registry path with a tag, or
// a digest.
//
// Two differences between those images are enforced rather than left to fail
// confusingly, because both produce failures that do not name the option
// responsible:
//
//   - MongoDB's own server images publish no bare version tags; every tag
//     there names an OS variant. A bare version applied to one of them is
//     refused with ErrNoSuchVersion rather than becoming a pull that 404s.
//   - Atlas Local's command is its entrypoint and it already runs a
//     single-node replica set. WithReplicaSet and WithMongodArgs are refused
//     with ErrUnsupportedForImage rather than becoming a container that exits
//     127 with an exec error naming the flag.
//
// An image this package does not know is assumed to behave like the official
// one, which is the permissive answer and the right one for a private mirror.
//
// One Atlas Local difference cannot be enforced and has to be expected
// instead: it restarts mongod while starting up, so a client that connects the
// instant Start returns will see one disconnection. See ImageAtlasLocal.
//
// # The docker client is an interface
//
// This package depends on [dockerclient.Client], never on a concrete client.
// The default is the standard-library client from dockerapi, and WithDocker
// takes any other: the moby wrapper, or one of the doubles in dockermock for
// a test that must not touch a daemon.
//
// # Ports are assigned by the daemon
//
// By default the create request publishes 27017 with an empty host port,
// which asks the daemon to pick one, and Start reads back what it picked.
// That is deliberate. Finding a free port and then binding it are two steps,
// and between them something else can take the port; parallel tests lose that
// race regularly. WithPort and GetAvailablePort exist for callers who need a
// port known in advance and can live with the window.
//
// # Readiness is not a TCP accept
//
// Docker's userland proxy binds the published port as soon as the container
// is created, before the entrypoint has run. A dial therefore succeeds
// against a container whose only process is runc init, and a caller that
// trusts it connects to nothing. Start polls the container's process listing
// until mongod itself is in it, and only then opens a connection.
//
// Be precise about what that guarantees, because the proxy accepts on
// mongod's behalf: the server process exists, the container has not died, and
// the address in the URI is live. It does not guarantee that mongod is
// answering MongoDB commands yet. Proving that needs a MongoDB conversation,
// so the driver layers above this package ping with backoff, and a caller
// using this package directly should expect its first command to need a
// retry.
//
// # The address is resolved, not assumed
//
// A container's published port is reachable at 127.0.0.1 only when this
// process and the daemon share a network namespace, which is the developer
// laptop case and nothing else. With a remote daemon the port is on that
// machine; with a socket bind-mounted into a CI container the containers are
// siblings and their ports are in the daemon host's namespace, not this one's.
// Start works the address out from where the daemon is and whether this
// process is itself containerised, and [Container.Endpoint] documents the
// order. WithHostIP and the MONGOTEST_HOST_IP environment variable are the
// escape hatch for a topology no detection covers.
//
// # Not here yet
//
// WithTLS currently marks the URI and nothing else: generating certificate
// material and copying it into the container lands with the rest of TLS
// support. Containers are not yet registered with a reaper, so a process
// killed between Start and Stop leaves its container behind; until then, the
// mongotest=regression label finds them:
//
//	docker rm -f $(docker ps -aq --filter label=mongotest=regression)
package mongod
