package mongod

import (
	"context"
	"strconv"
	"sync"

	"github.com/tophergopher/mongotest/dockerapi"
	"github.com/tophergopher/mongotest/dockerclient"
	"github.com/tophergopher/mongotest/reaper"
)

// Start creates a MongoDB container, starts it, and waits until mongod is
// reachable. If a step fails, the container is removed and the error names
// the step.
//
// With no options it runs mongo:8 on a host port the daemon chooses and waits
// up to a minute. Options are merged left to right, so a shared base can be
// specialised per call without being changed:
//
//	c, err := mongod.Start(ctx, base, mongod.WithReplicaSet("rs0"))
func Start(ctx context.Context, opts ...*Options) (*Container, error) {
	cfg, err := mergeAll(opts).Resolve()
	if err != nil {
		return nil, err
	}
	docker, err := cfg.client()
	if err != nil {
		return nil, err
	}
	// Worked out before anything is created, so failing to leaves no
	// container behind.
	address, err := cfg.hostResolver(docker.Host()).resolve()
	if err != nil {
		return nil, err
	}
	// The host decides which interface the port is published on, and so
	// whether the container needs credentials.
	host := dialHost(address.Host)
	cfg = cfg.withCredentialsIfExposed(host)

	id, err := createContainer(ctx, docker, cfg, host)
	if err != nil {
		return nil, err
	}
	c := newContainer(id, host, cfg, docker)
	if err := c.start(ctx, cfg, address.Source); err != nil {
		c.removeAfterFailedStart(ctx)
		return nil, err
	}
	// The logged URI hides the password: logs are shared more widely than
	// access to the daemon.
	c.logger.Info("mongod is running", "container", c.name, "id", shortID(id), "uri", c.uri(redactedPassword))
	return c, nil
}

// ReapRunningContainers removes every container Start created that has not
// been stopped, along with anything else registered with the reaper package.
// Call it after m.Run in TestMain so a run leaves nothing behind:
//
//	func TestMain(m *testing.M) {
//		code := m.Run()
//		if err := mongod.ReapRunningContainers(context.Background()); err != nil {
//			log.Print(err)
//		}
//		os.Exit(code)
//	}
//
// Every removal is attempted even if one fails, and the error names each
// container that is still there.
func ReapRunningContainers(ctx context.Context) error { return reaper.Reap(ctx) }

// defaultClient finds the daemon the way the docker CLI does, once for the
// process. A client keeps connections open and has no Close, so a client
// per start would leak one for every container.
var defaultClient = sync.OnceValues(dockerapi.FromEnv)

// client returns the caller's Docker client, or the shared default. This is
// the only place a concrete client is named.
func (r Resolved) client() (dockerclient.Client, error) {
	if r.Docker != nil {
		return r.Docker, nil
	}
	fromEnv, err := defaultClient()
	if err != nil {
		return nil, err
	}
	return fromEnv, nil
}

// createContainer creates the container, pulling the image only if the
// daemon does not have it, which keeps the usual case to one call.
func createContainer(ctx context.Context, docker dockerclient.Client, cfg Resolved, host string) (string, error) {
	image := cfg.Image.Reference()
	request := cfg.containerConfig(host)

	id, warnings, err := docker.ContainerCreate(ctx, cfg.Name, request)
	if dockerclient.IsNotFound(err) {
		cfg.Logger.Debug("image is not present locally, pulling it", "image", image)
		if err := docker.ImagePull(ctx, image); err != nil {
			return "", &StartError{Op: OpPullImage, Name: cfg.Name, Image: image, Err: err}
		}
		id, warnings, err = docker.ContainerCreate(ctx, cfg.Name, request)
	}
	if err != nil {
		return "", &StartError{Op: OpCreate, Name: cfg.Name, Image: image, Err: err}
	}
	for _, warning := range warnings {
		cfg.Logger.Warn("the daemon warned about the container it created", "container", cfg.Name, "warning", warning)
	}
	return id, nil
}

// newContainer describes a container that has just been created, and
// registers it for removal should the process be signalled.
//
// It registers now rather than once mongod is ready, because a signal during
// the readiness wait would otherwise leave the container running.
func newContainer(id, host string, cfg Resolved, docker dockerclient.Client) *Container {
	c := &Container{
		id: id, name: cfg.Name, image: cfg.Image, replicaSet: cfg.ReplicaSet,
		username: cfg.Username, password: cfg.Password,
		tls: cfg.TLS, host: host, startTimeout: cfg.StartTimeout,
		docker: docker, logger: cfg.Logger,
	}
	// Locked because the reaper may call Stop, which reads the handle, the
	// moment Register returns.
	c.mu.Lock()
	defer c.mu.Unlock()
	c.reaperHandle = reaper.Register(c.name, c.Stop)
	return c
}

// start runs the created container and waits for mongod. hostSource says
// where the dial address came from, for the error a failed wait returns.
func (c *Container) start(ctx context.Context, cfg Resolved, hostSource string) error {
	if cfg.TLS {
		c.logger.Warn("TLS mode is requested but no certificate material is copied yet, so mongod is still speaking plain TCP",
			"container", c.name)
	}
	if cfg.needsKeyFile() {
		if err := copyKeyFile(ctx, c.docker, c.id, c.Image()); err != nil {
			return c.startError(OpCopyKeyFile, err)
		}
	}

	c.logger.Debug("starting container", "container", c.name, "image", c.Image())
	if err := c.docker.ContainerStart(ctx, c.id); err != nil {
		return c.startError(OpStart, err)
	}
	info, err := c.docker.ContainerInspect(ctx, c.id)
	if err != nil {
		return c.startError(OpInspect, err)
	}
	c.ports = publishedPorts(info)
	port, published := c.ports[MongoPort]
	if !published {
		return &UnpublishedPortError{Port: MongoPort, Name: c.name, ID: c.id}
	}

	probe := readyProbe{
		docker: c.docker, logger: c.logger,
		id: c.id, name: c.name, host: c.host, port: port,
		hostSource: hostSource, timeout: cfg.StartTimeout,
		requireAuth: cfg.Username != "",
	}
	return probe.wait(ctx)
}

// startError reports which step of a start failed for this container.
func (c *Container) startError(op string, err error) error {
	return &StartError{Op: op, Name: c.name, Image: c.Image(), Err: err}
}

// removeAfterFailedStart removes the container a failed start left behind.
// The caller's context may be what failed, so the removal gets its own
// deadline instead.
func (c *Container) removeAfterFailedStart(ctx context.Context) {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), cleanupTimeout)
	defer cancel()
	if err := c.Stop(ctx); err != nil {
		c.logger.Error("cannot remove the container a failed start created", "container", c.name, "error", err)
	}
}

// publishedPorts maps each container port to the host port it is published
// on. A port with no host port is left out, so asking for it is an error
// rather than an address ending in :0.
func publishedPorts(info dockerclient.ContainerInspect) map[string]int {
	ports := map[string]int{}
	for containerPort := range info.NetworkSettings.Ports {
		port, err := strconv.Atoi(info.HostPort(containerPort))
		if err != nil || port <= 0 {
			continue
		}
		ports[containerPort] = port
	}
	return ports
}
