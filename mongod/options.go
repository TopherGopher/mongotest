package mongod

import (
	"crypto/rand"
	"encoding/hex"
	"maps"
	"net"
	"os"
	"strconv"
	"time"

	"github.com/tophergopher/mongotest/dockerclient"
)

// envImage names the environment variable that changes the default image. It
// is how CI pins an image without every test being changed.
const envImage = "MONGOTEST_IMAGE"

// Defaults every start uses unless an option says otherwise.
const (
	// defaultImage is the image a container runs when nothing else is asked
	// for.
	defaultImage = "mongo:8"
	// defaultStartTimeout bounds the wait for mongod to answer. On a slow
	// storage driver a container can take ten to fifteen seconds to start
	// when several start at once, so the budget is generous.
	defaultStartTimeout = 60 * time.Second
	// mongoPort is the port mongod listens on inside the container.
	mongoPort = "27017/tcp"
	// requiredLabel marks every container this package starts, so that a
	// stray one can be found and removed:
	// docker ps -aq --filter label=mongotest=regression
	requiredLabelKey   = "mongotest"
	requiredLabelValue = "regression"
	// cleanupTimeout bounds the removal of a container a failed start created.
	// The caller's own deadline cannot be reused, because it has usually
	// expired by the time the cleanup runs.
	cleanupTimeout = 30 * time.Second
	// namePrefix and nameRandomBytes shape the generated container name.
	namePrefix      = "mongotest-"
	nameRandomBytes = 4
)

// config is what the options build up. Start reads it once and never
// afterwards, so an option cannot change a container that already exists.
type config struct {
	image string
	// imageSet distinguishes "not asked for", which falls back to the
	// environment and then the default, from WithImage(""), which is an error
	// the caller wants to hear about rather than have quietly corrected.
	imageSet bool
	port     int
	name     string
	hostIP   string
	// hostIPExplicit distinguishes "not asked for" from WithHostIP(""), so
	// that an empty override is rejected rather than quietly ignored.
	hostIPExplicit bool
	labels         map[string]string
	replicaSet     string
	tls            bool
	mongodArgs     []string
	startTimeout   time.Duration
	logger         dockerclient.Logger
	docker         dockerclient.Client

	// The lookups address resolution needs. They are fields so that a test
	// can decide what this process looks like from the outside.
	getenv  func(string) string
	detect  func() Containerisation
	gateway func() (string, error)
}

// Option configures a container. Options are applied in the order they are
// given, so a later one overrides an earlier one.
type Option func(*config)

// WithImage runs a specific image instead of the default mongo:8. It
// overrides the MONGOTEST_IMAGE environment variable.
func WithImage(ref string) Option {
	return func(c *config) {
		c.image = ref
		c.imageSet = true
	}
}

// WithReplicaSet starts mongod with --replSet name, which is what makes
// transactions and change streams available.
//
// The container is only started in replica set mode; the replSetInitiate that
// brings the set up is done by the driver layers above this package, because
// it needs a MongoDB client and this package has none.
func WithReplicaSet(name string) Option {
	return func(c *config) { c.replicaSet = name }
}

// WithTLS marks the container as running in TLS mode, which puts &tls=true in
// the URI.
//
// Generating the certificate material and copying it into the container is
// not implemented yet; it lands with the rest of TLS support. Until then this
// option changes the URI and nothing else, and a container started with it
// still speaks plain TCP.
func WithTLS() Option {
	return func(c *config) { c.tls = true }
}

// WithPort publishes mongod on a fixed host port rather than one the daemon
// chooses.
//
// Prefer the default. Between finding a free port and a container binding it
// there is a window something else can take it, which is a race parallel
// tests lose regularly; letting the daemon assign the port closes it. Use
// this only when something outside the test needs a port known in advance.
func WithPort(port int) Option {
	return func(c *config) { c.port = port }
}

// WithLogger sends what a start does to a logger. The default discards
// everything. *slog.Logger satisfies the interface directly.
func WithLogger(l dockerclient.Logger) Option {
	return func(c *config) { c.logger = l }
}

// WithDocker uses a specific Docker client: the standard-library client, the
// moby wrapper, or one of the doubles in dockermock. The default is a client
// built from the environment the way the docker CLI finds its daemon.
func WithDocker(client dockerclient.Client) Option {
	return func(c *config) { c.docker = client }
}

// WithStartTimeout bounds the wait for mongod to become reachable. The
// default is 60 seconds.
func WithStartTimeout(d time.Duration) Option {
	return func(c *config) { c.startTimeout = d }
}

// WithLabel adds a label to the container. The mongotest=regression label is
// always present as well, so that containers this package started can be
// found whatever else is on them.
func WithLabel(key, value string) Option {
	return func(c *config) { c.labels[key] = value }
}

// WithMongodArgs passes extra flags to mongod, after the replica set flag.
// The image's entrypoint supplies the "mongod" itself, so pass flags only.
func WithMongodArgs(args ...string) Option {
	return func(c *config) { c.mongodArgs = append(c.mongodArgs, args...) }
}

// WithName names the container instead of generating one. A generated name is
// mongotest- followed by eight random hex characters, which is what keeps
// parallel starts from colliding, so pin a name only when something outside
// the test has to find the container by it.
func WithName(name string) Option {
	return func(c *config) { c.name = name }
}

// WithHostIP states the address containers started through this daemon are
// reachable at, instead of having it worked out.
//
// This is the escape hatch, and it is deliberate: no amount of detection
// covers every network topology. It overrides everything, including the
// MONGOTEST_HOST_IP environment variable, which is the same escape hatch for
// CI that cannot pass options.
func WithHostIP(host string) Option {
	return func(c *config) {
		c.hostIP = host
		c.hostIPExplicit = true
	}
}

// newConfig returns the configuration a start begins from.
func newConfig() config {
	return config{
		labels:       map[string]string{requiredLabelKey: requiredLabelValue},
		startTimeout: defaultStartTimeout,
		logger:       dockerclient.NopLogger(),
		getenv:       os.Getenv,
		detect:       DetectContainerisation,
		gateway:      realGateway,
	}
}

// finalise fills in what the options left unset and rejects what cannot work,
// before anything is created. An option that is wrong should cost nothing to
// find out about.
func (c *config) finalise() error {
	if !c.imageSet {
		c.image = c.getenv(envImage)
		if c.image == "" {
			c.image = defaultImage
		}
	}
	if c.image == "" {
		return ErrEmptyImage
	}
	if c.port < 0 || c.port > 65535 {
		return invalidPort(c.port)
	}
	if c.startTimeout <= 0 {
		return invalidStartTimeout(c.startTimeout)
	}
	if c.hostIPExplicit && c.hostIP == "" {
		return ErrEmptyHostIP
	}
	// The required label is set last so that WithLabel cannot drop it.
	c.labels[requiredLabelKey] = requiredLabelValue
	if c.name == "" {
		name, err := generateName()
		if err != nil {
			return err
		}
		c.name = name
	}
	return nil
}

// containerConfig builds the create request. The image entrypoint prepends
// mongod, so the command is flags only.
//
// resolvedHost is the address the caller will dial, and it decides the
// interface the port is published on; see bindIP.
func (c *config) containerConfig(resolvedHost string) dockerclient.ContainerConfig {
	var cmd []string
	if c.replicaSet != "" {
		cmd = append(cmd, "--replSet", c.replicaSet)
	}
	cmd = append(cmd, c.mongodArgs...)

	hostPort := ""
	if c.port != 0 {
		hostPort = strconv.Itoa(c.port)
	}
	return dockerclient.ContainerConfig{
		Image:        c.image,
		Cmd:          cmd,
		Labels:       maps.Clone(c.labels),
		ExposedPorts: map[string]struct{}{mongoPort: {}},
		HostConfig: &dockerclient.HostConfig{PortBindings: map[string][]dockerclient.PortBinding{
			// An empty HostPort asks the daemon to choose the port.
			mongoPort: {{HostIP: bindIP(resolvedHost), HostPort: hostPort}},
		}},
	}
}

// bindIP returns the interface to publish mongod on, given the address the
// caller is going to dial.
//
// This has to follow the resolution rather than being a constant. Docker
// takes HostIP literally: docker-proxy listens on that address alone, and the
// DNAT rule it installs matches that address alone. A port bound to
// 127.0.0.1 is therefore refused from every other interface, so resolving a
// bridge gateway or a remote host and then binding loopback produces a
// correct address for a port that is not listening there.
//
// Loopback stays loopback, which keeps the developer-laptop case off the
// machine's other interfaces: mongod here has no authentication, and there is
// nothing to gain by exposing it when the caller is on this machine anyway.
// Any other address widens the bind to every interface rather than to the
// resolved address itself, because the address the caller dials need not be
// one the daemon's host can bind: a remote daemon's hostname, or an address
// in front of a NAT, are both things only the caller can resolve.
func bindIP(resolvedHost string) string {
	if resolvedHost == "localhost" {
		return loopback
	}
	if ip := net.ParseIP(resolvedHost); ip != nil && ip.IsLoopback() {
		return loopback
	}
	return allInterfaces
}

// resolver returns the address resolution for this configuration, given where
// the client says the daemon is.
func (c *config) resolver(dockerHost string) hostResolver {
	return hostResolver{
		dockerHost: dockerHost,
		override:   c.hostIP,
		getenv:     c.getenv,
		detect:     c.detect,
		gateway:    c.gateway,
	}
}

// generateName returns mongotest- plus eight random hex characters. The
// randomness is what keeps parallel starts from colliding on a name the
// daemon would refuse as already in use.
func generateName() (string, error) {
	raw := make([]byte, nameRandomBytes)
	if _, err := rand.Read(raw); err != nil {
		return "", &nameError{Err: err}
	}
	return namePrefix + hex.EncodeToString(raw), nil
}
