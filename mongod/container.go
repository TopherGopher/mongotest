package mongod

import (
	"context"
	"net/url"
	"strconv"
	"sync"
	"time"

	"github.com/tophergopher/mongotest/dockerapi"
	"github.com/tophergopher/mongotest/dockerclient"
	"github.com/tophergopher/mongotest/reaper"
)

// Container is a running mongod. It is created by Start and removed by Stop,
// and it holds no MongoDB client: connecting is the caller's job, which is
// what keeps this package free of any driver dependency.
//
// Every method is safe to call concurrently, and Stop is safe to call twice
// and on a nil receiver.
type Container struct {
	id         string
	name       string
	image      MongoImage
	replicaSet string
	tls        bool
	// startTimeout is kept so a caller can see what budget was resolved.
	startTimeout time.Duration
	// host is the address resolved at start, not an assumption. Where the
	// daemon is decides it.
	host string
	// ports maps a container port such as "27017/tcp" to the host port the
	// daemon published it on.
	ports  map[string]int
	docker dockerclient.Client
	logger dockerclient.Logger

	// mu guards stopped and reaperHandle. Stop is called from defers, from
	// cleanup handlers and from the reaper, so it has to tolerate being called
	// at once from several of them.
	mu      sync.Mutex
	stopped bool
	// reaperHandle is the registration that tears this container down if the
	// process is signalled before Stop is called. Stop gives it back.
	reaperHandle reaper.Handle
}

// Start creates a mongod container, starts it and waits until it is
// reachable, or removes it again and returns why it is not.
//
// With no options it runs mongo:8, publishes 27017 on a host port the daemon
// chooses, labels the container mongotest=regression and waits up to a minute.
// Every one of those is an option, an environment variable, or both; see
// Options for how the three sources are ordered.
//
// Several Options are merged left to right, so a shared base can be
// specialised per case without being modified:
//
//	base := mongod.NewOptions().WithDocker(client).WithImage("8.0")
//	c, err := mongod.Start(ctx, base, mongod.WithReplicaSet("rs0"))
func Start(ctx context.Context, opts ...*Options) (*Container, error) {
	cfg, err := mergeAll(opts).Resolve()
	if err != nil {
		return nil, err
	}

	docker := cfg.Docker
	if docker == nil {
		// The one place this package names a concrete client: the default
		// when the caller supplied none. Everything else works through the
		// dockerclient.Client interface.
		fromEnv, err := dockerapi.FromEnv()
		if err != nil {
			return nil, err
		}
		docker = fromEnv
	}

	// Resolving first means an address this process cannot reach costs
	// nothing: no container is created to have to clean up again.
	host, err := cfg.resolver(docker.Host()).resolve()
	if err != nil {
		return nil, err
	}

	id, err := createContainer(ctx, docker, cfg, host)
	if err != nil {
		return nil, err
	}

	c := &Container{
		id: id, name: cfg.Name, image: cfg.Image, replicaSet: cfg.ReplicaSet,
		tls: cfg.TLS, host: host, startTimeout: cfg.StartTimeout,
		docker: docker, logger: cfg.Logger,
	}
	// Registered for teardown as soon as the container exists, not once it is
	// ready. The readiness wait runs for up to StartTimeout, and a signal
	// during it kills the process before the cleanup below can run, so a
	// container registered only on success leaks for that whole window.
	//
	// Registering this early is safe because the cleanup path goes through
	// Stop, which both removes the container and gives the registration back,
	// and which treats an already-removed container as success.
	c.reaperHandle = reaper.Register(cfg.Name, c.Stop)

	// Anything that fails from here on leaves a container behind, so it is
	// removed on the way out. Only the last statement clears this.
	started := false
	defer func() {
		if started {
			return
		}
		// The caller's context may be the reason this failed, so the cleanup
		// gets one that is not already cancelled. It still needs a deadline of
		// its own: nothing else bounds the removal, and a daemon that stalls
		// on it would turn a failed start into one that never returns.
		cleanupCtx, cancelCleanup := context.WithTimeout(context.WithoutCancel(ctx), cleanupTimeout)
		defer cancelCleanup()
		if err := c.Stop(cleanupCtx); err != nil {
			cfg.Logger.Error("cannot remove the container a failed start created", "container", cfg.Name, "error", err)
		}
	}()

	if cfg.TLS {
		// The certificate material and the mongod flags that use it are not
		// implemented yet; WithTLS currently marks the URI and nothing more.
		cfg.Logger.Warn("TLS mode is requested but no certificate material is copied yet, so mongod is still speaking plain TCP",
			"container", cfg.Name)
	}

	cfg.Logger.Debug("starting container", "container", cfg.Name, "image", cfg.Image.Reference())
	if err := docker.ContainerStart(ctx, id); err != nil {
		return nil, &StartError{Op: "start container", Name: cfg.Name, Image: cfg.Image.Reference(), Err: err}
	}

	info, err := docker.ContainerInspect(ctx, id)
	if err != nil {
		return nil, &StartError{Op: "inspect container", Name: cfg.Name, Image: cfg.Image.Reference(), Err: err}
	}
	c.ports = publishedPorts(info)
	port, ok := c.ports[MongoPort]
	if !ok {
		return nil, &UnpublishedPortError{Port: MongoPort, Name: cfg.Name, ID: id}
	}

	probe := readyProbe{
		docker: docker, logger: cfg.Logger,
		id: id, name: cfg.Name, host: host, port: port, timeout: cfg.StartTimeout,
	}
	if err := probe.wait(ctx); err != nil {
		return nil, err
	}

	cfg.Logger.Info("mongod is running", "container", cfg.Name, "id", shortID(id), "uri", c.URI())
	started = true
	return c, nil
}

// ReapRunningContainers removes every container that is still registered for
// teardown: one that Start created and that nothing has stopped yet.
//
// This is the explicit form of what the signal handler does, and it needs no
// signal. Defer it from a TestMain, or call it from a cleanup, to guarantee
// that a run leaves nothing behind even where it ends in a way no handler sees:
//
//	func TestMain(m *testing.M) {
//		code := m.Run()
//		if err := mongod.ReapRunningContainers(context.Background()); err != nil {
//			log.Print(err)
//		}
//		os.Exit(code)
//	}
//
// It reaps everything registered with the reaper package, which in a process
// whose registrations all come from here -- the normal case -- is every live
// container. Every teardown runs even if one fails; the error names each
// container that could not be removed.
func ReapRunningContainers(ctx context.Context) error { return reaper.Reap(ctx) }

// createContainer creates the container, pulling the image once if it is not
// present locally. Creating first and pulling only on a miss keeps the common
// case to a single call.
func createContainer(ctx context.Context, docker dockerclient.Client, cfg Resolved, resolvedHost string) (string, error) {
	request := cfg.containerConfig(resolvedHost)
	id, warnings, err := docker.ContainerCreate(ctx, cfg.Name, request)
	if dockerclient.IsNotFound(err) {
		cfg.Logger.Debug("image is not present locally, pulling it", "image", cfg.Image.Reference())
		if pullErr := docker.ImagePull(ctx, cfg.Image.Reference()); pullErr != nil {
			return "", &StartError{Op: "pull the image for", Name: cfg.Name, Image: cfg.Image.Reference(), Err: pullErr}
		}
		id, warnings, err = docker.ContainerCreate(ctx, cfg.Name, request)
	}
	if err != nil {
		return "", &StartError{Op: "create container", Name: cfg.Name, Image: cfg.Image.Reference(), Err: err}
	}
	// A warning does not stop a container from working, but it is often the
	// first sign of a misconfigured daemon, so it is not dropped silently.
	for _, warning := range warnings {
		cfg.Logger.Warn("the daemon warned about the container it created", "container", cfg.Name, "warning", warning)
	}
	return id, nil
}

// publishedPorts reads the host ports a container's ports were published on.
// A port with no host port is left out rather than recorded as zero, so that
// asking for it fails rather than handing back an address of :0.
func publishedPorts(info dockerclient.ContainerInspect) map[string]int {
	ports := map[string]int{}
	for containerPort := range info.NetworkSettings.Ports {
		hostPort := info.HostPort(containerPort)
		if hostPort == "" {
			continue
		}
		port, err := strconv.Atoi(hostPort)
		if err != nil || port <= 0 {
			continue
		}
		ports[containerPort] = port
	}
	return ports
}

// ID returns the container id the daemon assigned. Every call to the daemon is
// addressed to it; [Container.Name] is the readable half.
//
//	3f1a9c4b7e2d5a8f0b6c3e9d1a4f7b2c5e8d0a3f6b9c2e5d8a1f4b7c0e3d6a9f
func (c *Container) ID() string { return c.id }

// Name returns the container's name: the one WithName asked for, or a generated
// mongotest- plus eight random hex characters.
//
//	mongotest-1a2b3c4d
func (c *Container) Name() string { return c.name }

// Image returns the reference of the image the container runs, with every part
// resolved from the option, the environment and the defaults.
//
//	mongo:8
//	public.ecr.aws/docker/library/mongo:8.0
//	mongodb/mongodb-community-server:8.0-ubi9
//
// [Container.MongoImage] is the same thing in parts.
func (c *Container) Image() string { return c.image.Reference() }

// MongoImage returns the image the container runs, in its three parts. Use it
// when the registry, repository or version matters on its own;
// [Container.Image] is the rendered reference.
//
//	Registry:   "public.ecr.aws"
//	Repository: "docker/library/mongo"
//	Version:    "8.0"
//
// An empty Registry means Docker Hub, which is how "mongo:8" stays the short
// form everyone recognises.
func (c *Container) MongoImage() MongoImage { return c.image }

// StartTimeout returns the readiness budget this container was started with,
// after WithStartTimeout, MONGOTEST_START_TIMEOUT and the default were taken
// into account.
//
//	1m0s
func (c *Container) StartTimeout() time.Duration { return c.startTimeout }

// ReplicaSet returns the replica set name mongod was started with, or "" for a
// standalone container. The driver layers read it to decide whether to run
// replSetInitiate.
//
//	rs0   // started with WithReplicaSet("rs0")
//	      // standalone, so nothing to initiate
//
// Empty does not always mean standalone: Atlas Local runs a set of its own
// under a generated name, which has to be read from the server. See
// [MongoImage.ReplicaSetPreconfigured].
func (c *Container) ReplicaSet() string { return c.replicaSet }

// Port returns the host port mongod is published on: the one WithPort pinned,
// or the one the daemon assigned.
//
//	32768
//
// Pair it with [Container.Host]; [Container.Endpoint] returns both, and is the
// one to use for any port other than 27017.
func (c *Container) Port() int { return c.ports[MongoPort] }

// Host returns the address mongod is reachable at from this process.
//
//	127.0.0.1    // local daemon, this process not containerised
//	172.17.0.1   // local socket, this process in a sibling container
//	build-host   // DOCKER_HOST=tcp://build-host:2376
//
// It is not always loopback. A container's published port lands on the machine
// running the daemon, so a remote daemon means a remote address, and a process
// that is itself containerised does not share the daemon host's network
// namespace. See [Container.Endpoint] for the whole rule.
func (c *Container) Host() string { return c.host }

// URI returns the MongoDB connection string for this container, ready to hand
// to a driver.
//
//	mongodb://127.0.0.1:32768/?directConnection=true
//	mongodb://127.0.0.1:32768/?directConnection=true&tls=true   // WithTLS
//	mongodb://172.17.0.1:32768/?directConnection=true           // sibling container
//
// directConnection stops the driver from trying to discover a topology a single
// container does not have; without it a replica set member that advertises
// itself as localhost:27017 sends the driver somewhere it cannot reach.
func (c *Container) URI() string {
	uri := url.URL{Scheme: "mongodb", Host: addr(c.host, c.Port()), Path: "/"}
	query := "directConnection=true"
	if c.tls {
		// A driver connecting to a server in TLS mode has to be told so, or
		// the handshake fails with an error that explains nothing.
		query += "&tls=true"
	}
	uri.RawQuery = query
	return uri.String()
}

// Endpoint reports the address to dial to reach containerPort ("27017/tcp"),
// taking into account where the daemon is and whether this process is itself
// containerised.
//
//	host, port, err := c.Endpoint("27017/tcp")   // "127.0.0.1", 32768, nil
//	_, _, err = c.Endpoint("28017/tcp")          // ErrNoPublishedPort
//
// The address is resolved once, when the container starts, in this order:
//
//  1. the WithHostIP option, which overrides everything;
//  2. the MONGOTEST_HOST_IP environment variable, for CI that cannot pass
//     options;
//  3. the hostname of a tcp://, http://, https:// or ssh:// daemon, because a
//     published port lands on the machine running the daemon;
//  4. loopback, for a unix socket or named pipe when this process is not
//     containerised;
//  5. the default route's gateway, for a unix socket when it is: the
//     containers are siblings on the daemon's host, and their published ports
//     are in that host's network namespace rather than this one's.
func (c *Container) Endpoint(containerPort string) (host string, port int, err error) {
	published, ok := c.ports[containerPort]
	if !ok {
		return "", 0, &UnpublishedPortError{Port: containerPort, Name: c.name, ID: c.id}
	}
	return c.host, published, nil
}

// Stop removes the container, forcing it down and taking its anonymous
// volumes with it.
//
// It is idempotent and safe on a nil receiver, because it is what a deferred
// cleanup calls without knowing whether the start got that far. A container
// someone else already removed counts as success. A removal the daemon
// refuses is reported and does not mark the container stopped, so the caller
// can try again.
func (c *Container) Stop(ctx context.Context) error {
	if c == nil {
		return nil
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.stopped {
		return nil
	}

	err := c.docker.ContainerRemove(ctx, c.id, dockerclient.RemoveOptions{Force: true, RemoveVolumes: true})
	if err != nil && !dockerclient.IsNotFound(err) {
		return err
	}
	c.stopped = true
	// Given back only on success, so that a removal the daemon refused is
	// still attempted by a later reap.
	reaper.Unregister(c.reaperHandle)
	c.logger.Debug("container removed", "container", c.name, "id", shortID(c.id))
	return nil
}
