package mongod

import (
	"context"
	"net"
	"net/url"
	"strconv"
	"sync"

	"github.com/tophergopher/mongotest/dockerapi"
	"github.com/tophergopher/mongotest/dockerclient"
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
	image      string
	replicaSet string
	tls        bool
	// host is the address resolved at start, not an assumption. Where the
	// daemon is decides it.
	host string
	// ports maps a container port such as "27017/tcp" to the host port the
	// daemon published it on.
	ports  map[string]int
	docker dockerclient.Client
	logger dockerclient.Logger

	// mu guards stopped. Stop is called from defers, from cleanup handlers
	// and from a reaper, so it has to tolerate being called at once from
	// several of them.
	mu      sync.Mutex
	stopped bool
}

// Start creates a mongod container, starts it and waits until it is
// reachable, or removes it again and returns why it is not.
//
// With no options it runs mongo:8 (or MONGOTEST_IMAGE), publishes 27017 on a
// host port the daemon chooses, labels the container mongotest=regression and
// waits up to a minute. The Docker client is built from the environment
// unless WithDocker supplies one.
func Start(ctx context.Context, opts ...Option) (*Container, error) {
	cfg := newConfig()
	for _, opt := range opts {
		if opt != nil {
			opt(&cfg)
		}
	}
	if err := cfg.finalise(); err != nil {
		return nil, err
	}

	docker := cfg.docker
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

	id, err := createContainer(ctx, docker, cfg)
	if err != nil {
		return nil, err
	}

	c := &Container{
		id: id, name: cfg.name, image: cfg.image, replicaSet: cfg.replicaSet,
		tls: cfg.tls, host: host,
		docker: docker, logger: cfg.logger,
	}
	// Anything that fails from here on leaves a container behind, so it is
	// removed on the way out. Only the last statement clears this.
	started := false
	defer func() {
		if started {
			return
		}
		// The caller's context may be the reason this failed, so cleanup gets
		// one that is not already cancelled.
		if err := c.Stop(context.WithoutCancel(ctx)); err != nil {
			cfg.logger.Error("cannot remove the container a failed start created", "container", cfg.name, "error", err)
		}
	}()

	if cfg.tls {
		// The certificate material and the mongod flags that use it are not
		// implemented yet; WithTLS currently marks the URI and nothing more.
		cfg.logger.Warn("TLS mode is requested but no certificate material is copied yet, so mongod is still speaking plain TCP",
			"container", cfg.name)
	}

	cfg.logger.Debug("starting container", "container", cfg.name, "image", cfg.image)
	if err := docker.ContainerStart(ctx, id); err != nil {
		return nil, &StartError{Op: "start container", Name: cfg.name, Image: cfg.image, Err: err}
	}

	info, err := docker.ContainerInspect(ctx, id)
	if err != nil {
		return nil, &StartError{Op: "inspect container", Name: cfg.name, Image: cfg.image, Err: err}
	}
	c.ports = publishedPorts(info)
	port, ok := c.ports[mongoPort]
	if !ok {
		return nil, &UnpublishedPortError{Port: mongoPort, Name: cfg.name, ID: id}
	}

	probe := readyProbe{
		docker: docker, logger: cfg.logger,
		id: id, name: cfg.name, host: host, port: port, timeout: cfg.startTimeout,
	}
	if err := probe.wait(ctx); err != nil {
		return nil, err
	}

	cfg.logger.Info("mongod is running", "container", cfg.name, "id", shortID(id), "uri", c.URI())
	started = true
	return c, nil
}

// createContainer creates the container, pulling the image once if it is not
// present locally. Creating first and pulling only on a miss keeps the common
// case to a single call.
func createContainer(ctx context.Context, docker dockerclient.Client, cfg config) (string, error) {
	request := cfg.containerConfig()
	id, warnings, err := docker.ContainerCreate(ctx, cfg.name, request)
	if dockerclient.IsNotFound(err) {
		cfg.logger.Debug("image is not present locally, pulling it", "image", cfg.image)
		if pullErr := docker.ImagePull(ctx, cfg.image); pullErr != nil {
			return "", &StartError{Op: "pull the image for", Name: cfg.name, Image: cfg.image, Err: pullErr}
		}
		id, warnings, err = docker.ContainerCreate(ctx, cfg.name, request)
	}
	if err != nil {
		return "", &StartError{Op: "create container", Name: cfg.name, Image: cfg.image, Err: err}
	}
	// A warning does not stop a container from working, but it is often the
	// first sign of a misconfigured daemon, so it is not dropped silently.
	for _, warning := range warnings {
		cfg.logger.Warn("the daemon warned about the container it created", "container", cfg.name, "warning", warning)
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

// ID returns the container id the daemon assigned.
func (c *Container) ID() string { return c.id }

// Name returns the container's name.
func (c *Container) Name() string { return c.name }

// Image returns the image the container runs.
func (c *Container) Image() string { return c.image }

// ReplicaSet returns the replica set name mongod was started with, or "" for
// a standalone container. The driver layers read it to decide whether to run
// replSetInitiate.
func (c *Container) ReplicaSet() string { return c.replicaSet }

// Port returns the host port mongod is published on.
func (c *Container) Port() int { return c.ports[mongoPort] }

// Host returns the address mongod is reachable at from this process.
//
// It is not always loopback. A container's published port lands on the
// machine running the daemon, so a remote daemon means a remote address, and
// a process that is itself containerised does not share the daemon host's
// network namespace. See Endpoint for the whole rule.
func (c *Container) Host() string { return c.host }

// URI returns the MongoDB connection string for this container.
//
// directConnection stops the driver from trying to discover a topology a
// single container does not have; without it a replica set member that
// advertises itself as localhost:27017 sends the driver somewhere it cannot
// reach.
func (c *Container) URI() string {
	uri := url.URL{Scheme: "mongodb", Host: net.JoinHostPort(c.host, strconv.Itoa(c.Port())), Path: "/"}
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
	c.logger.Debug("container removed", "container", c.name, "id", shortID(c.id))
	return nil
}
