// Package mongod starts and stops MongoDB containers.
//
// It knows nothing about the MongoDB wire protocol and imports no driver,
// which is what lets the driver v1 root module and the v2 module share it.
// What it offers is a container that is running, published, and proven to be
// answering; connecting to it is the caller's job.
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
// minute for mongod to answer. Every part of that is an option: see WithImage,
// WithReplicaSet, WithPort, WithMongodArgs, WithLabel and WithStartTimeout.
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
// until mongod is in it, and only then opens a connection.
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
// # Environment variables
//
//	MONGOTEST_IMAGE     the image to run, overriding mongo:8 but not WithImage
//	MONGOTEST_HOST_IP   the address containers are reachable at, overriding
//	                    detection but not WithHostIP
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
