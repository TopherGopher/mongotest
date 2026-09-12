package mongod

import (
	"crypto/rand"
	"encoding/hex"
	"maps"
	"net"
	"strconv"
	"time"

	"github.com/tophergopher/mongotest/dockerclient"
)

// Environment variables, for configuring a whole suite without editing code.
// Each one is overridden by the matching option, and overrides the default.
const (
	// EnvImage is a whole image reference in any shape ParseMongoImage
	// accepts: "mongo:8", "8.0", "public.ecr.aws/docker/library/mongo:8".
	EnvImage = "MONGOTEST_IMAGE"
	// EnvImageRegistry, EnvImageRepository and EnvImageVersion set one part
	// each, and override the corresponding part of EnvImage. Setting the
	// registry alone is how a CI job moves off Docker Hub without touching
	// the version its tests pin.
	EnvImageRegistry   = "MONGOTEST_IMAGE_REGISTRY"
	EnvImageRepository = "MONGOTEST_IMAGE_REPOSITORY"
	EnvImageVersion    = "MONGOTEST_IMAGE_VERSION"
	// EnvPort pins the host port, for an environment that has to know it in
	// advance. Prefer leaving it unset; see Options.Port.
	EnvPort = "MONGOTEST_PORT"
	// EnvStartTimeout raises or lowers the readiness budget, which a slow CI
	// runner usually needs to do for every test at once. Any value
	// time.ParseDuration accepts: "90s", "2m".
	EnvStartTimeout = "MONGOTEST_START_TIMEOUT"
	// EnvHostIP states the address containers are reachable at, overriding
	// detection. See Container.Endpoint for when that is needed.
	EnvHostIP = "MONGOTEST_HOST_IP"
)

// Defaults every start uses unless an option or the environment says
// otherwise.
const (
	// DefaultStartTimeout bounds the wait for mongod to answer. On a slow
	// storage driver a container can take ten to fifteen seconds to start
	// when several start at once, so the budget is generous.
	DefaultStartTimeout = 60 * time.Second
	// MongoPort is the port mongod listens on inside the container.
	MongoPort = "27017/tcp"
	// RequiredLabelKey and RequiredLabelValue mark every container this
	// package starts, so a stray one can be found and removed:
	//
	//	docker rm -f $(docker ps -aq --filter label=mongotest=regression)
	RequiredLabelKey   = "mongotest"
	RequiredLabelValue = "regression"

	// cleanupTimeout bounds the removal of a container a failed start
	// created. The caller's own deadline cannot be reused, because it has
	// usually expired by the time the cleanup runs.
	cleanupTimeout = 30 * time.Second
	// namePrefix and nameRandomBytes shape the generated container name.
	namePrefix      = "mongotest-"
	nameRandomBytes = 4
)

// field names the settings whose presence has to be distinguished from their
// zero value, so that an option explicitly set to a zero value still beats the
// environment. Port is the one that matters in practice: 0 is a real choice
// ("let the daemon pick"), not an absence.
type field int

const (
	// The image has a marker per part, because "set to empty" means something
	// different for each: an empty registry is Docker Hub, while an empty
	// repository names nothing at all.
	fieldImage field = iota
	fieldImageRef
	fieldRegistry
	fieldRepository
	fieldVersion
	fieldPort
	fieldName
	fieldHostIP
	fieldReplicaSet
	fieldTLS
	fieldMongodArgs
	fieldStartTimeout
	fieldLogger
	fieldDocker
	fieldLabels
)

// Options is everything a container can be told. Build it in whichever style
// suits the call site; all three produce the same container.
//
// Chained from a package-level helper, which is the terse form:
//
//	c, err := mongod.Start(ctx, mongod.WithImage("8.0").WithPort(27018))
//
// Chained from NewOptions, which reads better when there are several:
//
//	opts := mongod.NewOptions().
//		WithMongoImage(mongod.ImageCommunity.WithVersion("8.0-ubi8")).
//		WithReplicaSet("rs0").
//		WithStartTimeout(90 * time.Second)
//
// Or written out, which is what a table-driven test usually wants:
//
//	opts := &mongod.Options{Image: mongod.ImageAtlasLocal, Port: 27018}
//
// The fields are public and finer-grained than the helpers: WithImage takes a
// reference in any shape and works out which parts it set, while the fields
// let each part be set on its own.
//
// Nothing is parsed, inferred or validated while the options are being built.
// That happens in Resolve, which Start calls, so a helper never returns an
// error and the whole configuration is checked in one place before anything is
// created. Call Resolve yourself to see what a set of options means.
//
// An Options is not safe to modify from several goroutines at once, but a
// resolved one is never held: Start takes a snapshot, so one Options can be
// shared as a base by parallel tests that each specialise it.
type Options struct {
	// Image is where the image comes from, what it is called and which
	// version. Any part left empty is filled in from the environment and then
	// from the default. See MongoImage and the well-known images.
	Image MongoImage
	// ImageRef is an image reference in any shape ParseMongoImage accepts,
	// parsed during Resolve and merged under Image. WithImage sets this; it
	// exists so that parsing happens with everything else rather than inside
	// a helper that cannot report a failure.
	ImageRef string
	// Port is the host port to publish mongod on. Zero lets the daemon
	// choose, which is what avoids the race between finding a free port and
	// binding it; prefer it. See WithPort.
	Port int
	// Name is the container name. Empty generates mongotest- plus eight
	// random hex characters, which is what keeps parallel starts from
	// colliding.
	Name string
	// HostIP states the address containers are reachable at, instead of
	// having it worked out. The escape hatch; see Container.Endpoint.
	HostIP string
	// ReplicaSet runs mongod with --replSet, which is what makes
	// transactions and change streams available. The driver layers above this
	// package do the replSetInitiate. Refused for an image that runs a
	// replica set of its own; see ImageAtlasLocal.
	ReplicaSet string
	// TLS marks the container as running in TLS mode, which puts &tls=true in
	// the URI. The certificate material is not generated yet.
	TLS bool
	// MongodArgs are extra flags for mongod, after the replica set flag. The
	// image's entrypoint supplies the program, so pass flags only. Refused
	// for an image whose command is its entrypoint; see ImageAtlasLocal.
	MongodArgs []string
	// StartTimeout bounds the wait for mongod to become reachable. Zero takes
	// EnvStartTimeout, then DefaultStartTimeout.
	StartTimeout time.Duration
	// Labels are attached to the container. RequiredLabelKey is always added,
	// so containers this package started can be found whatever else is on
	// them.
	Labels map[string]string
	// Logger receives what a start does. Nil discards it. *slog.Logger
	// satisfies the interface directly.
	Logger dockerclient.Logger
	// Docker is the client to use. Nil builds one from the environment the
	// way the docker CLI finds its daemon.
	Docker dockerclient.Client

	// set records which fields a With helper touched, so that a value that
	// happens to be the zero value is still known to be a decision. A field
	// assigned directly on a struct literal is not recorded, which is why a
	// zero field there falls back to the environment.
	set map[field]bool
	// lookups are the filesystem and environment readers, so a test can
	// decide what this process looks like from the outside. Nil means the
	// real ones.
	getenv  func(string) string
	detect  func() Containerisation
	gateway func() (string, error)
}

// NewOptions returns an empty Options ready to be chained.
func NewOptions() *Options { return &Options{} }

// clone returns a copy that shares no mutable state with the receiver, so
// that merging never writes through to a caller's Options.
func (o *Options) clone() *Options {
	if o == nil {
		return &Options{}
	}
	out := *o
	out.MongodArgs = append([]string(nil), o.MongodArgs...)
	out.Labels = maps.Clone(o.Labels)
	out.set = maps.Clone(o.set)
	return &out
}

// mark records that a field was set explicitly and returns the receiver, so
// the helpers stay one line each.
func (o *Options) mark(f field) *Options {
	if o.set == nil {
		o.set = map[field]bool{}
	}
	o.set[f] = true
	return o
}

// isSet reports whether a With helper set this field.
func (o *Options) isSet(f field) bool { return o.set[f] }

// The package-level helpers each start a chain by returning a fresh Options,
// so that mongod.WithImage("8").WithPort(0) reads as one expression and
// Start(ctx, mongod.WithDocker(c), mongod.WithPort(p)) still composes.

// WithImage takes an image reference in any shape ParseMongoImage accepts: a
// version on its own ("8.0"), a repository ("mongo"), both ("mongo:8.0"), a
// full path with a registry and tag, or a digest. Whatever it does not name is
// filled in during Resolve.
func WithImage(ref string) *Options { return NewOptions().WithImage(ref) }

// WithMongoImage sets the image from its parts, usually one of the well-known
// images with a version of your choosing.
func WithMongoImage(img MongoImage) *Options { return NewOptions().WithMongoImage(img) }

// WithRegistry pulls from a different registry, keeping the repository and
// version. This is how a known image becomes a private mirror of itself.
func WithRegistry(registry string) *Options { return NewOptions().WithRegistry(registry) }

// WithRepository sets the repository path, keeping the registry and version.
func WithRepository(repository string) *Options { return NewOptions().WithRepository(repository) }

// WithVersion sets the version, keeping where the image comes from. It accepts
// a tag or a digest.
func WithVersion(version string) *Options { return NewOptions().WithVersion(version) }

// WithPort publishes mongod on a fixed host port rather than one the daemon
// chooses. Prefer the default: between finding a free port and a container
// binding it there is a window something else can take it, which parallel
// tests lose regularly. WithPort(0) asks for a daemon-assigned port
// explicitly, which also suppresses EnvPort.
func WithPort(port int) *Options { return NewOptions().WithPort(port) }

// WithName names the container instead of generating one.
func WithName(name string) *Options { return NewOptions().WithName(name) }

// WithHostIP states the address containers are reachable at. See
// Container.Endpoint for the resolution it overrides.
func WithHostIP(host string) *Options { return NewOptions().WithHostIP(host) }

// WithReplicaSet starts mongod with --replSet name.
func WithReplicaSet(name string) *Options { return NewOptions().WithReplicaSet(name) }

// WithTLS marks the container as running in TLS mode.
func WithTLS() *Options { return NewOptions().WithTLS() }

// WithMongodArgs passes extra flags to mongod, after the replica set flag.
func WithMongodArgs(args ...string) *Options { return NewOptions().WithMongodArgs(args...) }

// WithStartTimeout bounds the wait for mongod to become reachable.
func WithStartTimeout(d time.Duration) *Options { return NewOptions().WithStartTimeout(d) }

// WithLabel adds a label to the container.
func WithLabel(key, value string) *Options { return NewOptions().WithLabel(key, value) }

// WithLogger sends what a start does to a logger.
func WithLogger(l dockerclient.Logger) *Options { return NewOptions().WithLogger(l) }

// WithDocker uses a specific Docker client: the standard-library client, the
// moby wrapper, or one of the doubles in dockermock.
func WithDocker(client dockerclient.Client) *Options { return NewOptions().WithDocker(client) }

// The methods continue a chain. Each returns the receiver, so they can be
// strung together in any order.

// WithImage sets the image from a reference in any shape. See the
// package-level WithImage.
func (o *Options) WithImage(ref string) *Options {
	o.ImageRef = ref
	return o.mark(fieldImageRef)
}

// WithMongoImage sets the image from its parts.
func (o *Options) WithMongoImage(img MongoImage) *Options {
	o.Image = img
	return o.mark(fieldImage)
}

// WithRegistry pulls from a different registry, keeping the other parts.
func (o *Options) WithRegistry(registry string) *Options {
	o.Image.Registry = registry
	return o.mark(fieldRegistry)
}

// WithRepository sets the repository path, keeping the other parts.
func (o *Options) WithRepository(repository string) *Options {
	o.Image.Repository = repository
	return o.mark(fieldRepository)
}

// WithVersion sets the version, keeping where the image comes from.
func (o *Options) WithVersion(version string) *Options {
	o.Image.Version = version
	return o.mark(fieldVersion)
}

// WithPort publishes mongod on a fixed host port. See the package-level
// WithPort for why the default is usually better.
func (o *Options) WithPort(port int) *Options {
	o.Port = port
	return o.mark(fieldPort)
}

// WithName names the container instead of generating one.
func (o *Options) WithName(name string) *Options {
	o.Name = name
	return o.mark(fieldName)
}

// WithHostIP states the address containers are reachable at.
func (o *Options) WithHostIP(host string) *Options {
	o.HostIP = host
	return o.mark(fieldHostIP)
}

// WithReplicaSet starts mongod with --replSet name.
func (o *Options) WithReplicaSet(name string) *Options {
	o.ReplicaSet = name
	return o.mark(fieldReplicaSet)
}

// WithTLS marks the container as running in TLS mode.
func (o *Options) WithTLS() *Options {
	o.TLS = true
	return o.mark(fieldTLS)
}

// WithMongodArgs appends extra flags for mongod.
func (o *Options) WithMongodArgs(args ...string) *Options {
	o.MongodArgs = append(o.MongodArgs, args...)
	return o.mark(fieldMongodArgs)
}

// WithStartTimeout bounds the wait for mongod to become reachable.
func (o *Options) WithStartTimeout(d time.Duration) *Options {
	o.StartTimeout = d
	return o.mark(fieldStartTimeout)
}

// WithLabel adds a label to the container.
func (o *Options) WithLabel(key, value string) *Options {
	if o.Labels == nil {
		o.Labels = map[string]string{}
	}
	o.Labels[key] = value
	return o.mark(fieldLabels)
}

// WithLogger sends what a start does to a logger.
func (o *Options) WithLogger(l dockerclient.Logger) *Options {
	o.Logger = l
	return o.mark(fieldLogger)
}

// WithDocker uses a specific Docker client.
func (o *Options) WithDocker(client dockerclient.Client) *Options {
	o.Docker = client
	return o.mark(fieldDocker)
}

// merge returns the receiver overlaid with later, field by field. A field
// later set explicitly wins; so does one it holds a non-zero value for, which
// is what makes a struct literal work as an override.
func (o *Options) merge(later *Options) *Options {
	out := o.clone()
	if later == nil {
		return out
	}
	// The image is merged part by part, so that overriding the version does
	// not discard a registry the base had set. Each part carries its own
	// marker, so a part deliberately set to empty survives the merge.
	if later.isSet(fieldImageRef) || later.ImageRef != "" {
		out.ImageRef = later.ImageRef
		out.mark(fieldImageRef)
	}
	if later.isSet(fieldImage) || !later.Image.IsZero() {
		out.Image = overlay(out.Image, later.Image)
		out.mark(fieldImage)
	}
	if later.isSet(fieldRegistry) {
		out.Image.Registry = later.Image.Registry
		out.mark(fieldRegistry)
	}
	if later.isSet(fieldRepository) {
		out.Image.Repository = later.Image.Repository
		out.mark(fieldRepository)
	}
	if later.isSet(fieldVersion) {
		out.Image.Version = later.Image.Version
		out.mark(fieldVersion)
	}
	if later.isSet(fieldPort) || later.Port != 0 {
		out.Port = later.Port
		out.mark(fieldPort)
	}
	if later.isSet(fieldName) || later.Name != "" {
		out.Name = later.Name
		out.mark(fieldName)
	}
	if later.isSet(fieldHostIP) || later.HostIP != "" {
		out.HostIP = later.HostIP
		out.mark(fieldHostIP)
	}
	if later.isSet(fieldReplicaSet) || later.ReplicaSet != "" {
		out.ReplicaSet = later.ReplicaSet
		out.mark(fieldReplicaSet)
	}
	if later.isSet(fieldTLS) || later.TLS {
		out.TLS = later.TLS
		out.mark(fieldTLS)
	}
	if len(later.MongodArgs) > 0 {
		out.MongodArgs = append(out.MongodArgs, later.MongodArgs...)
		out.mark(fieldMongodArgs)
	}
	if later.isSet(fieldStartTimeout) || later.StartTimeout != 0 {
		out.StartTimeout = later.StartTimeout
		out.mark(fieldStartTimeout)
	}
	for key, value := range later.Labels {
		if out.Labels == nil {
			out.Labels = map[string]string{}
		}
		out.Labels[key] = value
		out.mark(fieldLabels)
	}
	if later.Logger != nil {
		out.Logger = later.Logger
		out.mark(fieldLogger)
	}
	if later.Docker != nil {
		out.Docker = later.Docker
		out.mark(fieldDocker)
	}
	if later.getenv != nil {
		out.getenv = later.getenv
	}
	if later.detect != nil {
		out.detect = later.detect
	}
	if later.gateway != nil {
		out.gateway = later.gateway
	}
	return out
}

// mergeAll overlays a list of Options left to right, leaving every one of them
// untouched.
func mergeAll(opts []*Options) *Options {
	merged := &Options{}
	for _, o := range opts {
		merged = merged.merge(o)
	}
	return merged
}

// generateName returns mongotest- plus eight random hex characters. The
// randomness is what keeps parallel starts from colliding on a name the daemon
// would refuse as already in use.
func generateName() (string, error) {
	raw := make([]byte, nameRandomBytes)
	if _, err := rand.Read(raw); err != nil {
		return "", &nameError{Err: err}
	}
	return namePrefix + hex.EncodeToString(raw), nil
}

// containerConfig builds the create request from resolved settings. The
// image's entrypoint supplies mongod, so the command is flags only.
//
// resolvedHost is the address the caller will dial, and it decides the
// interface the port is published on; see bindIP.
func (r Resolved) containerConfig(resolvedHost string) dockerclient.ContainerConfig {
	var cmd []string
	// An image whose command is its entrypoint must be left alone: overriding
	// Cmd there replaces the program rather than configuring it. Resolve has
	// already refused the options that would have produced one.
	if r.Image.AcceptsMongodArgs() {
		if r.ReplicaSet != "" {
			cmd = append(cmd, "--replSet", r.ReplicaSet)
		}
		cmd = append(cmd, r.MongodArgs...)
	}

	hostPort := ""
	if r.Port != 0 {
		hostPort = strconv.Itoa(r.Port)
	}
	return dockerclient.ContainerConfig{
		Image:        r.Image.Reference(),
		Cmd:          cmd,
		Labels:       maps.Clone(r.Labels),
		ExposedPorts: map[string]struct{}{MongoPort: {}},
		HostConfig: &dockerclient.HostConfig{PortBindings: map[string][]dockerclient.PortBinding{
			// An empty HostPort asks the daemon to choose the port.
			MongoPort: {{HostIP: bindIP(resolvedHost), HostPort: hostPort}},
		}},
	}
}

// bindIP returns the interface to publish mongod on, given the address the
// caller is going to dial.
//
// This has to follow the resolution rather than being a constant. Docker takes
// HostIP literally: docker-proxy listens on that address alone, and the DNAT
// rule it installs matches that address alone. A port bound to 127.0.0.1 is
// therefore refused from every other interface, so resolving a bridge gateway
// or a remote host and then binding loopback produces a correct address for a
// port that is not listening there.
//
// Loopback stays loopback, which keeps the developer-laptop case off the
// machine's other interfaces: mongod here has no authentication, and there is
// nothing to gain by exposing it when the caller is on this machine anyway.
// Any other address widens the bind to every interface rather than to the
// resolved address itself, because the address the caller dials need not be
// one the daemon's host can bind: a remote daemon's hostname, or an address in
// front of a NAT, are both things only the caller can resolve.
func bindIP(resolvedHost string) string {
	if resolvedHost == "localhost" {
		return loopback
	}
	if ip := net.ParseIP(resolvedHost); ip != nil && ip.IsLoopback() {
		return loopback
	}
	return allInterfaces
}

// resolver returns the address resolution for these settings, given where the
// client says the daemon is.
func (r Resolved) resolver(dockerHost string) hostResolver {
	return hostResolver{
		dockerHost: dockerHost,
		override:   r.HostIP,
		getenv:     r.getenv,
		detect:     r.detect,
		gateway:    r.gateway,
	}
}
