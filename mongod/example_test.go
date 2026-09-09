package mongod_test

import (
	"context"
	"errors"
	"fmt"
	"net"
	"strconv"
	"strings"
	"time"

	"github.com/tophergopher/mongotest/dockerclient"
	"github.com/tophergopher/mongotest/dockermock"
	"github.com/tophergopher/mongotest/mongod"
)

// The examples in this file start containers on an in-memory stand-in for the
// Docker daemon (see exampleDaemon at the bottom of the file), so they run
// anywhere and produce the same output with no Docker installed. Real code
// leaves WithDocker and WithPort out and gets a real daemon and a
// daemon-assigned port instead:
//
//	c, err := mongod.Start(ctx)
func ExampleStart() {
	docker, port, cleanup := exampleDaemon()
	defer cleanup()

	c, err := mongod.Start(context.Background(), mongod.WithDocker(docker), mongod.WithPort(port))
	if err != nil {
		fmt.Println("cannot start mongod:", err)
		return
	}
	defer func() { _ = c.Stop(context.Background()) }()

	fmt.Println("running", c.Image())
	fmt.Println("connect with", strings.Replace(c.URI(), strconv.Itoa(c.Port()), "<port>", 1))
	// Output:
	// running mongo:8
	// connect with mongodb://127.0.0.1:<port>/?directConnection=true
}

func ExampleStart_replicaSet() {
	docker, port, cleanup := exampleDaemon()
	defer cleanup()

	// The container runs mongod --replSet rs0. Bringing the set up with
	// replSetInitiate needs a MongoDB client, so the driver layers above this
	// package do it.
	c, err := mongod.Start(context.Background(),
		mongod.WithDocker(docker),
		mongod.WithPort(port),
		mongod.WithReplicaSet("rs0"),
	)
	if err != nil {
		fmt.Println("cannot start mongod:", err)
		return
	}
	defer func() { _ = c.Stop(context.Background()) }()

	fmt.Println("replica set:", c.ReplicaSet())
	// Output: replica set: rs0
}

func ExampleContainer_URI() {
	docker, port, cleanup := exampleDaemon()
	defer cleanup()
	c, cleanupContainer := exampleContainer(docker, port)
	defer cleanupContainer()

	// directConnection stops the driver from discovering a topology a single
	// container does not have.
	fmt.Println(strings.Replace(c.URI(), strconv.Itoa(c.Port()), "<port>", 1))
	// Output: mongodb://127.0.0.1:<port>/?directConnection=true
}

func ExampleContainer_Endpoint() {
	docker, port, cleanup := exampleDaemon()
	defer cleanup()
	c, cleanupContainer := exampleContainer(docker, port)
	defer cleanupContainer()

	host, published, err := c.Endpoint("27017/tcp")
	if err != nil {
		fmt.Println("no address for 27017/tcp:", err)
		return
	}
	fmt.Println("dial", host, "on the published port:", published == c.Port())

	// A port that was never published has no address, and saying so is better
	// than handing back one that goes nowhere.
	if _, _, err := c.Endpoint("28017/tcp"); errors.Is(err, mongod.ErrNoPublishedPort) {
		fmt.Println("28017/tcp is not published")
	}
	// Output:
	// dial 127.0.0.1 on the published port: true
	// 28017/tcp is not published
}

func ExampleContainer_Host() {
	docker, port, cleanup := exampleDaemon()
	defer cleanup()
	c, cleanupContainer := exampleContainer(docker, port)
	defer cleanupContainer()

	// Loopback here because the daemon is local and this process is not
	// containerised. A tcp:// daemon would report its own host instead.
	fmt.Println(c.Host())
	// Output: 127.0.0.1
}

func ExampleContainer_Port() {
	docker, port, cleanup := exampleDaemon()
	defer cleanup()
	c, cleanupContainer := exampleContainer(docker, port)
	defer cleanupContainer()

	fmt.Println("published on the port that was pinned:", c.Port() == port)
	// Output: published on the port that was pinned: true
}

func ExampleContainer_ID() {
	docker, port, cleanup := exampleDaemon()
	defer cleanup()
	c, cleanupContainer := exampleContainer(docker, port)
	defer cleanupContainer()

	fmt.Println("the daemon assigned an id:", c.ID() != "")
	// Output: the daemon assigned an id: true
}

func ExampleContainer_Name() {
	docker, port, cleanup := exampleDaemon()
	defer cleanup()

	c, err := mongod.Start(context.Background(),
		mongod.WithDocker(docker), mongod.WithPort(port), mongod.WithName("mongotest-example"))
	if err != nil {
		fmt.Println("cannot start mongod:", err)
		return
	}
	defer func() { _ = c.Stop(context.Background()) }()

	fmt.Println(c.Name())
	// Output: mongotest-example
}

func ExampleContainer_Image() {
	docker, port, cleanup := exampleDaemon()
	defer cleanup()
	c, cleanupContainer := exampleContainer(docker, port)
	defer cleanupContainer()

	fmt.Println(c.Image())
	// Output: mongo:8
}

func ExampleContainer_ReplicaSet() {
	docker, port, cleanup := exampleDaemon()
	defer cleanup()
	c, cleanupContainer := exampleContainer(docker, port)
	defer cleanupContainer()

	// Empty for a standalone container, which is what makes it the test the
	// driver layers use to decide whether to run replSetInitiate.
	fmt.Printf("standalone: %q\n", c.ReplicaSet())
	// Output: standalone: ""
}

func ExampleContainer_Stop() {
	docker, port, cleanup := exampleDaemon()
	defer cleanup()
	fake := docker.(*dockermock.Fake)

	c, err := mongod.Start(context.Background(), mongod.WithDocker(fake), mongod.WithPort(port))
	if err != nil {
		fmt.Println("cannot start mongod:", err)
		return
	}

	fmt.Println("stopped:", c.Stop(context.Background()))
	// Calling it again is what a deferred cleanup does after an explicit
	// stop, so it has to be harmless.
	fmt.Println("stopped again:", c.Stop(context.Background()))
	fmt.Println("containers left behind:", len(fake.Containers()))
	// Output:
	// stopped: <nil>
	// stopped again: <nil>
	// containers left behind: 0
}

func ExampleWithImage() {
	docker, port, cleanup := exampleDaemon()
	defer cleanup()

	c, err := mongod.Start(context.Background(),
		mongod.WithDocker(docker), mongod.WithPort(port), mongod.WithImage("mongo:8.0-noble"))
	if err != nil {
		fmt.Println("cannot start mongod:", err)
		return
	}
	defer func() { _ = c.Stop(context.Background()) }()

	fmt.Println(c.Image())
	// Output: mongo:8.0-noble
}

func ExampleWithReplicaSet() {
	docker, port, cleanup := exampleDaemon()
	defer cleanup()
	fake := docker.(*dockermock.Fake)

	c, err := mongod.Start(context.Background(),
		mongod.WithDocker(fake), mongod.WithPort(port), mongod.WithReplicaSet("rs0"))
	if err != nil {
		fmt.Println("cannot start mongod:", err)
		return
	}
	defer func() { _ = c.Stop(context.Background()) }()

	// The image entrypoint supplies the "mongod", so the command is flags.
	fmt.Println(fake.Containers()[0].Config.Cmd)
	// Output: [--replSet rs0]
}

func ExampleWithMongodArgs() {
	docker, port, cleanup := exampleDaemon()
	defer cleanup()
	fake := docker.(*dockermock.Fake)

	c, err := mongod.Start(context.Background(),
		mongod.WithDocker(fake), mongod.WithPort(port),
		mongod.WithReplicaSet("rs0"),
		mongod.WithMongodArgs("--setParameter", "enableTestCommands=1"),
	)
	if err != nil {
		fmt.Println("cannot start mongod:", err)
		return
	}
	defer func() { _ = c.Stop(context.Background()) }()

	// Extra arguments follow the replica set flag.
	fmt.Println(fake.Containers()[0].Config.Cmd)
	// Output: [--replSet rs0 --setParameter enableTestCommands=1]
}

func ExampleWithLabel() {
	docker, port, cleanup := exampleDaemon()
	defer cleanup()
	fake := docker.(*dockermock.Fake)

	c, err := mongod.Start(context.Background(),
		mongod.WithDocker(fake), mongod.WithPort(port), mongod.WithLabel("suite", "checkout"))
	if err != nil {
		fmt.Println("cannot start mongod:", err)
		return
	}
	defer func() { _ = c.Stop(context.Background()) }()

	labels := fake.Containers()[0].Config.Labels
	fmt.Println("suite:", labels["suite"])
	// mongotest=regression is always there, so a stray container can be found
	// whatever else is on it.
	fmt.Println("mongotest:", labels["mongotest"])
	// Output:
	// suite: checkout
	// mongotest: regression
}

func ExampleWithName() {
	docker, port, cleanup := exampleDaemon()
	defer cleanup()

	// Without this the name is mongotest- plus eight random hex characters,
	// which is what keeps parallel starts from colliding.
	c, err := mongod.Start(context.Background(),
		mongod.WithDocker(docker), mongod.WithPort(port), mongod.WithName("mongotest-checkout"))
	if err != nil {
		fmt.Println("cannot start mongod:", err)
		return
	}
	defer func() { _ = c.Stop(context.Background()) }()

	fmt.Println(c.Name())
	// Output: mongotest-checkout
}

func ExampleWithPort() {
	docker, _, cleanup := exampleDaemon()
	defer cleanup()

	// Prefer the default, which is a port the daemon assigns. Between finding
	// a free port and a container binding it, something else can take it.
	port, err := mongod.GetAvailablePort()
	if err != nil {
		fmt.Println("cannot find a free port:", err)
		return
	}
	listener, err := net.Listen("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(port)))
	if err != nil {
		fmt.Println("cannot listen:", err)
		return
	}
	defer func() { _ = listener.Close() }()

	c, err := mongod.Start(context.Background(), mongod.WithDocker(docker), mongod.WithPort(port))
	if err != nil {
		fmt.Println("cannot start mongod:", err)
		return
	}
	defer func() { _ = c.Stop(context.Background()) }()

	fmt.Println("published where it was asked to be:", c.Port() == port)
	// Output: published where it was asked to be: true
}

func ExampleWithDocker() {
	// Any dockerclient.Client: the standard-library client, the moby wrapper,
	// or a double. This is a Fake, an in-memory daemon that enforces the real
	// daemon's rules without one being installed.
	fake := dockermock.NewFake(dockermock.WithImages("mongo:8"))
	fake.Processes = func(dockermock.ContainerState) [][]string {
		return [][]string{{"1", "mongod --bind_ip_all"}}
	}
	port, cleanup := exampleListener()
	defer cleanup()

	c, err := mongod.Start(context.Background(), mongod.WithDocker(fake), mongod.WithPort(port))
	if err != nil {
		fmt.Println("cannot start mongod:", err)
		return
	}
	defer func() { _ = c.Stop(context.Background()) }()

	fmt.Println("containers on the fake daemon:", len(fake.Containers()))
	// Output: containers on the fake daemon: 1
}

func ExampleWithStartTimeout() {
	docker, cleanup := exampleFake()
	defer cleanup()
	// A port with nothing on it, so that readiness can never succeed and the
	// only thing that ends the wait is the timeout.
	port, err := mongod.GetAvailablePort()
	if err != nil {
		fmt.Println("cannot find a free port:", err)
		return
	}

	_, err = mongod.Start(context.Background(),
		mongod.WithDocker(docker), mongod.WithPort(port), mongod.WithStartTimeout(200*time.Millisecond))

	fmt.Println("gave up waiting:", errors.Is(err, mongod.ErrNotReady))
	fmt.Println("because it ran out of time:", errors.Is(err, context.DeadlineExceeded))
	// Output:
	// gave up waiting: true
	// because it ran out of time: true
}

func ExampleWithLogger() {
	docker, port, cleanup := exampleDaemon()
	defer cleanup()

	// Satisfied directly by *slog.Logger. This one prints only the messages,
	// so the example's output does not move about.
	c, err := mongod.Start(context.Background(),
		mongod.WithDocker(docker), mongod.WithPort(port), mongod.WithLogger(messageLogger{}))
	if err != nil {
		fmt.Println("cannot start mongod:", err)
		return
	}
	defer func() { _ = c.Stop(context.Background()) }()
	// Output: mongod is running
}

func ExampleWithTLS() {
	docker, port, cleanup := exampleDaemon()
	defer cleanup()

	// The certificate material is not generated yet, so for now this marks
	// the URI and nothing else.
	c, err := mongod.Start(context.Background(),
		mongod.WithDocker(docker), mongod.WithPort(port), mongod.WithTLS())
	if err != nil {
		fmt.Println("cannot start mongod:", err)
		return
	}
	defer func() { _ = c.Stop(context.Background()) }()

	fmt.Println("the driver is told to use TLS:", strings.Contains(c.URI(), "tls=true"))
	// Output: the driver is told to use TLS: true
}

func ExampleWithHostIP() {
	docker, port, cleanup := exampleDaemon()
	defer cleanup()

	// The escape hatch: no detection covers every network topology, so a
	// caller who knows where the containers are can say so. MONGOTEST_HOST_IP
	// does the same for CI that cannot pass options.
	c, err := mongod.Start(context.Background(),
		mongod.WithDocker(docker), mongod.WithPort(port), mongod.WithHostIP("127.0.0.1"))
	if err != nil {
		fmt.Println("cannot start mongod:", err)
		return
	}
	defer func() { _ = c.Stop(context.Background()) }()

	fmt.Println(c.Host())
	// Output: 127.0.0.1
}

func ExampleGetAvailablePort() {
	port, err := mongod.GetAvailablePort()
	if err != nil {
		fmt.Println("cannot find a free port:", err)
		return
	}

	fmt.Println("got a port to pin with WithPort:", port > 0 && port <= 65535)
	// Output: got a port to pin with WithPort: true
}

func ExampleDetectContainerisation() {
	where := mongod.DetectContainerisation()

	// What it decides depends on where this runs, so only the part that is
	// true everywhere is printed: the answer always says what it was based
	// on, which is what makes a wrong one diagnosable.
	fmt.Println("the result explains itself:", where.Signal != "")
	if where.Containerised {
		// Ports published by a sibling container land on the daemon's host,
		// so Start dials the default route's gateway rather than loopback.
		_ = where.Signal
	}
	// Output: the result explains itself: true
}

// exampleDaemon returns an in-memory stand-in for the Docker daemon and a host
// port to pin, along with the function that releases both.
//
// Two things a real daemon supplies have to be supplied here: a process
// listing containing mongod, and something actually listening on the
// published port. Start waits for both, because a container's port is bound
// before its entrypoint has run and a dial alone proves nothing.
func exampleDaemon() (dockerclient.Client, int, func()) {
	docker, closeFake := exampleFake()
	port, closeListener := exampleListener()
	return docker, port, func() {
		closeListener()
		closeFake()
	}
}

// exampleFake returns the stand-in daemon on its own, for an example that
// wants a port nothing is listening on.
func exampleFake() (dockerclient.Client, func()) {
	fake := dockermock.NewFake(dockermock.WithImages("mongo:8"))
	fake.Processes = func(dockermock.ContainerState) [][]string {
		return [][]string{{"1", "mongod --bind_ip_all"}}
	}
	return fake, func() {}
}

// exampleListener stands in for mongod itself: a socket on loopback that
// accepts the connection Start's readiness probe opens.
func exampleListener() (int, func()) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		panic(err)
	}
	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			_ = conn.Close()
		}
	}()
	return listener.Addr().(*net.TCPAddr).Port, func() { _ = listener.Close() }
}

// exampleContainer starts a container for an example that is about one of the
// reporting methods rather than about starting.
func exampleContainer(docker dockerclient.Client, port int) (*mongod.Container, func()) {
	c, err := mongod.Start(context.Background(), mongod.WithDocker(docker), mongod.WithPort(port))
	if err != nil {
		panic(err)
	}
	return c, func() { _ = c.Stop(context.Background()) }
}

// messageLogger prints log messages and drops the key/value pairs, so that an
// example's output stays the same from one run to the next.
type messageLogger struct{}

func (messageLogger) Debug(_ string, _ ...any)   {}
func (messageLogger) Info(msg string, _ ...any)  { fmt.Println(msg) }
func (messageLogger) Warn(_ string, _ ...any)    {}
func (messageLogger) Error(msg string, _ ...any) { fmt.Println(msg) }
