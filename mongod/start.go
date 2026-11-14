package mongod

import (
	"context"
	"strconv"
	"sync"

	"github.com/tophergopher/mongotest/dockerapi"
	"github.com/tophergopher/mongotest/dockerclient"
	"github.com/tophergopher/mongotest/internal/reaper"
)

// Start creates a MongoDB container, starts it, and waits until mongod is
// reachable. If a step fails, the container is removed and the error names
// the step.
//
// With no options it runs mongo:8.0 on a host port the daemon chooses and
// waits up to a minute.
func Start(ctx context.Context, opts ...Option) (*Container, error) {
	cfg, err := newConfig(opts)
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
// been stopped. Call it after m.Run in TestMain so a run leaves nothing
// behind:
//
//	func TestMain(m *testing.M) {
//		code := m.Run()
//		if err := mongod.ReapRunningContainers(context.Background()); err != nil {
//			log.Print(err)
//		}
//		os.Exit(code)
//	}
//
// Every removal is attempted even if one fails. The error matches ErrReap
// and names each container that is still there.
func ReapRunningContainers(ctx context.Context) error { return reaper.Reap(ctx) }

// defaultClient finds the daemon the way the docker CLI does, once for the
// process. A client keeps connections open and has no Close, so a client
// per start would leak one for every container.
var defaultClient = sync.OnceValues(dockerapi.FromEnv)

// client returns the caller's Docker client, or the shared default. This is
// the only place a concrete client is named.
func (c config) client() (dockerclient.Client, error) {
	if c.docker != nil {
		return c.docker, nil
	}
	fromEnv, err := defaultClient()
	if err != nil {
		return nil, err
	}
	return fromEnv, nil
}

// createContainer creates the container, pulling the image only if the
// daemon does not have it, which keeps the usual case to one call.
func createContainer(ctx context.Context, docker dockerclient.Client, cfg config, host string) (string, error) {
	image := cfg.image.Reference()
	request := cfg.containerConfig(host)

	id, warnings, err := docker.ContainerCreate(ctx, cfg.name, request)
	if dockerclient.IsNotFound(err) {
		cfg.logger.Debug("image is not present locally, pulling it", "image", image)
		if err := docker.ImagePull(ctx, image); err != nil {
			return "", &StartError{Op: OpPullImage, Name: cfg.name, Image: image, Err: err}
		}
		id, warnings, err = docker.ContainerCreate(ctx, cfg.name, request)
	}
	if err != nil {
		return "", &StartError{Op: OpCreate, Name: cfg.name, Image: image, Err: err}
	}
	for _, warning := range warnings {
		cfg.logger.Warn("the daemon warned about the container it created", "container", cfg.name, "warning", warning)
	}
	return id, nil
}

// newContainer describes a container that has just been created, and
// registers it for removal should the process be signalled.
//
// It registers now rather than once mongod is ready, because a signal during
// the readiness wait would otherwise leave the container running.
func newContainer(id, host string, cfg config, docker dockerclient.Client) *Container {
	c := &Container{
		id: id, name: cfg.name, image: cfg.image, replicaSet: cfg.replicaSet,
		username: cfg.username, password: cfg.password,
		tls: cfg.tls, host: host, docker: docker, logger: cfg.logger,
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
func (c *Container) start(ctx context.Context, cfg config, hostSource string) error {
	if cfg.tls {
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
	port, published := hostPort(info)
	if !published {
		return &UnpublishedPortError{Port: mongoPort, Name: c.name, ID: c.id}
	}
	c.port = port

	probe := readyProbe{
		docker: c.docker, logger: c.logger,
		id: c.id, name: c.name, host: c.host, port: port,
		hostSource: hostSource, timeout: cfg.startTimeout,
		requireAuth: cfg.username != "",
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

// hostPort returns the host port mongod's port is published on, and whether
// the daemon published it at all.
func hostPort(info dockerclient.ContainerInspect) (int, bool) {
	port, err := strconv.Atoi(info.HostPort(mongoPort))
	if err != nil || port <= 0 {
		return 0, false
	}
	return port, true
}
