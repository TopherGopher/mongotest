package mongod

import (
	"crypto/rand"
	"encoding/hex"
	"maps"
	"slices"
	"time"

	"github.com/tophergopher/mongotest/dockerclient"
)

// Environment variables that configure a whole suite without editing code.
// The matching option overrides each one; each one overrides the default.
const (
	// EnvImage is an image reference in any shape ParseMongoImage accepts.
	EnvImage = "MONGOTEST_IMAGE"
	// EnvImageRegistry, EnvImageRepository and EnvImageVersion each set one
	// part of the image, overriding that part of EnvImage.
	EnvImageRegistry   = "MONGOTEST_IMAGE_REGISTRY"
	EnvImageRepository = "MONGOTEST_IMAGE_REPOSITORY"
	EnvImageVersion    = "MONGOTEST_IMAGE_VERSION"
	// EnvPort pins the host port. Prefer leaving it unset; see WithPort.
	EnvPort = "MONGOTEST_PORT"
	// EnvStartTimeout is how long to wait for mongod, as a Go duration: "90s".
	EnvStartTimeout = "MONGOTEST_START_TIMEOUT"
	// EnvHostIP is the address containers are reachable at; see WithHostIP.
	EnvHostIP = "MONGOTEST_HOST_IP"
)

// Defaults a start uses unless an option or the environment says otherwise.
const (
	// DefaultStartTimeout is how long Start waits for mongod.
	DefaultStartTimeout = 60 * time.Second
	// MongoPort is the port mongod listens on inside the container.
	MongoPort = "27017/tcp"
	// RequiredLabelKey and RequiredLabelValue mark every container this
	// package starts, so a stray one can be found and removed:
	//
	//	docker rm -f $(docker ps -aq --filter label=mongotest=regression)
	RequiredLabelKey   = "mongotest"
	RequiredLabelValue = "regression"
	// DefaultUsername is the root user when credentials are generated.
	DefaultUsername = "mongotest"

	// cleanupTimeout bounds removing the container a failed start created.
	cleanupTimeout = 30 * time.Second
	// namePrefix and nameRandomBytes shape a generated container name.
	namePrefix      = "mongotest-"
	nameRandomBytes = 4
	// secretRandomBytes sizes a generated password and replica set key.
	secretRandomBytes = 16
)

// field names a setting a With helper has touched. It is how an option set
// to its zero value, such as WithPort(0), is told apart from one never set,
// and so still overrides the environment.
type field int

const (
	fieldImage field = iota
	fieldRegistry
	fieldRepository
	fieldVersion
	fieldPort
	fieldName
	fieldHostIP
	fieldReplicaSet
	fieldAuth
	fieldTLS
	fieldMongodArgs
	fieldStartTimeout
	fieldLogger
	fieldDocker
	fieldLabels
)

// Options configures a container. Every field is optional, and there are
// three ways to build one:
//
//	mongod.Start(ctx, mongod.WithImage("8.0").WithReplicaSet("rs0"))     // chained
//	mongod.Start(ctx, &mongod.Options{Image: mongod.ImageAtlasLocal})    // literal
//	mongod.Start(ctx, base, mongod.WithReplicaSet("rs0"))                // merged
//
// A With method changes the Options it is called on and returns it, so
// base.WithPort(1) changes base. To specialise a shared base without changing
// it, pass both to Start as above or call [Options.Clone] first. Do not call
// With methods on one Options from several goroutines.
//
// A literal cannot say "zero on purpose": a field left at zero falls back to
// the environment, where WithPort(0) would not.
//
// Nothing is checked until [Options.Resolve], which Start calls.
type Options struct {
	// Image is the image to run. Empty parts come from the environment, then
	// the defaults.
	Image MongoImage
	// Port is the host port to publish on. Zero lets the daemon choose.
	Port int
	// Name is the container name. Empty generates one.
	Name string
	// HostIP overrides the address the container is dialled at.
	HostIP string
	// ReplicaSet starts mongod with --replSet under this name.
	ReplicaSet string
	// Username and Password create a root user; set both. See WithAuth.
	Username string
	Password string
	// NoAuth stops credentials being generated. See WithoutAuth.
	NoAuth bool
	// TLS adds tls=true to the URI. Certificates are not set up yet.
	TLS bool
	// MongodArgs are extra flags for mongod.
	MongodArgs []string
	// StartTimeout is how long to wait for mongod. Zero means the default.
	StartTimeout time.Duration
	// Labels are added to the container alongside the required label.
	Labels map[string]string
	// Logger receives what a start does. *slog.Logger works. Nil discards.
	Logger dockerclient.Logger
	// Docker is the client to use. Nil finds the daemon as the docker CLI does.
	Docker dockerclient.Client

	// set records the fields a With helper touched; see field.
	set map[field]bool
	// errs holds what the helpers could not parse. A chain has nowhere to
	// return an error, so Resolve reports the first one.
	errs []error
	// The lookups address resolution uses, replaced in tests. Nil means the
	// real ones.
	getenv  func(string) string
	detect  func() Containerisation
	gateway func() (string, error)
	bridge  func() (string, bool)
}

// NewOptions returns an empty Options to chain from.
func NewOptions() *Options { return &Options{} }

// Clone returns an independent copy, so a shared base can be specialised
// without changing it. Cloning nil gives an empty Options.
//
//	rs := base.Clone().WithReplicaSet("rs0")
func (o *Options) Clone() *Options {
	if o == nil {
		return &Options{}
	}
	out := *o
	out.MongodArgs = slices.Clone(o.MongodArgs)
	out.Labels = maps.Clone(o.Labels)
	out.set = maps.Clone(o.set)
	out.errs = slices.Clone(o.errs)
	return &out
}

// mark records that a With helper set a field.
func (o *Options) mark(f field) *Options {
	if o.set == nil {
		o.set = map[field]bool{}
	}
	o.set[f] = true
	return o
}

// isSet reports whether a With helper set a field.
func (o *Options) isSet(f field) bool { return o.set[f] }

// Each package-level helper starts a chain; the method of the same name
// continues one.

// WithImage sets the image from a reference in any shape ParseMongoImage
// accepts: "8.0", "mongo", "mongo:8.0", a full registry path, or a digest.
// Only the parts the reference names are set.
func WithImage(ref string) *Options { return NewOptions().WithImage(ref) }

// WithMongoImage sets the image from its three parts, usually one of the
// known images. A part left empty, the registry included, comes from the
// environment and then the default.
func WithMongoImage(img MongoImage) *Options { return NewOptions().WithMongoImage(img) }

// WithRegistry changes where the image is pulled from, keeping the rest.
func WithRegistry(registry string) *Options { return NewOptions().WithRegistry(registry) }

// WithRepository changes the image's repository, keeping the rest.
func WithRepository(repository string) *Options { return NewOptions().WithRepository(repository) }

// WithVersion changes the image's tag or digest, keeping the rest.
func WithVersion(version string) *Options { return NewOptions().WithVersion(version) }

// WithPort publishes mongod on a fixed host port. Prefer the default: a port
// found free can be taken before the container binds it, which parallel
// tests hit regularly. WithPort(0) asks for the default explicitly.
func WithPort(port int) *Options { return NewOptions().WithPort(port) }

// WithName names the container instead of generating a name.
func WithName(name string) *Options { return NewOptions().WithName(name) }

// WithHostIP states the address or hostname containers are reachable at, for
// a network the detection gets wrong. Without it Start uses the first of:
//
//  1. the MONGOTEST_HOST_IP environment variable;
//  2. the daemon's own host, when the daemon is remote (tcp, http, https, ssh);
//  3. loopback, when this process shares the daemon host's network;
//  4. the default gateway, when this process is in a container with a network
//     of its own, because the port is published on the host outside it.
func WithHostIP(host string) *Options { return NewOptions().WithHostIP(host) }

// WithReplicaSet starts mongod as a replica set member with this name. The
// caller still has to initiate the set.
func WithReplicaSet(name string) *Options { return NewOptions().WithReplicaSet(name) }

// WithAuth creates a root user and makes mongod require authentication.
// [Container.URI] then includes the credentials.
//
// Creating the user makes a start a few seconds slower, so by default
// credentials are only generated for a container other machines can reach.
// Anyone who can use the Docker daemon can read them, so do not reuse a
// password that matters.
func WithAuth(username, password string) *Options {
	return NewOptions().WithAuth(username, password)
}

// WithoutAuth runs mongod without authentication even where other machines
// can reach it. Use it only on a network you trust.
func WithoutAuth() *Options { return NewOptions().WithoutAuth() }

// WithTLS adds tls=true to the URI. Certificates are not set up yet, so a
// driver cannot connect to a container started with it.
func WithTLS() *Options { return NewOptions().WithTLS() }

// WithMongodArgs passes extra flags to mongod.
func WithMongodArgs(args ...string) *Options { return NewOptions().WithMongodArgs(args...) }

// WithStartTimeout sets how long Start waits for mongod.
func WithStartTimeout(d time.Duration) *Options { return NewOptions().WithStartTimeout(d) }

// WithLabel adds a label to the container.
func WithLabel(key, value string) *Options { return NewOptions().WithLabel(key, value) }

// WithLogger sends what a start does to a logger. A reap on a signal logs
// through reaper.SetLogger instead.
func WithLogger(l dockerclient.Logger) *Options { return NewOptions().WithLogger(l) }

// WithDocker uses a specific Docker client, such as a double from dockermock.
func WithDocker(client dockerclient.Client) *Options { return NewOptions().WithDocker(client) }

// WithImage sets the image from a reference. See [WithImage].
func (o *Options) WithImage(ref string) *Options {
	parsed, err := ParseMongoImage(ref)
	if err != nil {
		o.errs = append(o.errs, err)
		return o
	}
	if parsed.Registry != "" {
		o.WithRegistry(parsed.Registry)
	}
	if parsed.Repository != "" {
		o.WithRepository(parsed.Repository)
	}
	if parsed.Version != "" {
		o.WithVersion(parsed.Version)
	}
	return o
}

// WithMongoImage sets the image, replacing any earlier one. See
// [WithMongoImage].
func (o *Options) WithMongoImage(img MongoImage) *Options {
	o.Image = img
	// Earlier per-part choices no longer describe this image.
	delete(o.set, fieldRegistry)
	delete(o.set, fieldRepository)
	delete(o.set, fieldVersion)
	return o.mark(fieldImage)
}

// WithRegistry changes where the image is pulled from. See [WithRegistry].
func (o *Options) WithRegistry(registry string) *Options {
	o.Image.Registry = registry
	return o.mark(fieldRegistry)
}

// WithRepository changes the image's repository. See [WithRepository].
func (o *Options) WithRepository(repository string) *Options {
	o.Image.Repository = repository
	return o.mark(fieldRepository)
}

// WithVersion changes the image's tag or digest. See [WithVersion].
func (o *Options) WithVersion(version string) *Options {
	o.Image.Version = version
	return o.mark(fieldVersion)
}

// WithPort publishes mongod on a fixed host port. See [WithPort].
func (o *Options) WithPort(port int) *Options {
	o.Port = port
	return o.mark(fieldPort)
}

// WithName names the container. See [WithName].
func (o *Options) WithName(name string) *Options {
	o.Name = name
	return o.mark(fieldName)
}

// WithHostIP states the address containers are reachable at. See [WithHostIP].
func (o *Options) WithHostIP(host string) *Options {
	o.HostIP = host
	return o.mark(fieldHostIP)
}

// WithReplicaSet starts mongod as a replica set member. See [WithReplicaSet].
func (o *Options) WithReplicaSet(name string) *Options {
	o.ReplicaSet = name
	return o.mark(fieldReplicaSet)
}

// WithAuth creates a root user. See [WithAuth].
func (o *Options) WithAuth(username, password string) *Options {
	o.Username, o.Password, o.NoAuth = username, password, false
	return o.mark(fieldAuth)
}

// WithoutAuth runs mongod without authentication. See [WithoutAuth].
func (o *Options) WithoutAuth() *Options {
	o.Username, o.Password, o.NoAuth = "", "", true
	return o.mark(fieldAuth)
}

// WithTLS adds tls=true to the URI. See [WithTLS].
func (o *Options) WithTLS() *Options {
	o.TLS = true
	return o.mark(fieldTLS)
}

// WithMongodArgs appends extra flags for mongod. See [WithMongodArgs].
func (o *Options) WithMongodArgs(args ...string) *Options {
	o.MongodArgs = append(o.MongodArgs, args...)
	return o.mark(fieldMongodArgs)
}

// WithStartTimeout sets how long Start waits. See [WithStartTimeout].
func (o *Options) WithStartTimeout(d time.Duration) *Options {
	o.StartTimeout = d
	return o.mark(fieldStartTimeout)
}

// WithLabel adds a label to the container. See [WithLabel].
func (o *Options) WithLabel(key, value string) *Options {
	if o.Labels == nil {
		o.Labels = map[string]string{}
	}
	o.Labels[key] = value
	return o.mark(fieldLabels)
}

// WithLogger sends what a start does to a logger. See [WithLogger].
func (o *Options) WithLogger(l dockerclient.Logger) *Options {
	o.Logger = l
	return o.mark(fieldLogger)
}

// WithDocker uses a specific Docker client. See [WithDocker].
func (o *Options) WithDocker(client dockerclient.Client) *Options {
	o.Docker = client
	return o.mark(fieldDocker)
}

// mergeAll overlays Options left to right, changing none of them.
func mergeAll(opts []*Options) *Options {
	merged := &Options{}
	for _, o := range opts {
		merged = merged.merge(o)
	}
	return merged
}

// merge returns a copy of o with later laid over it. A field wins if later
// set it with a helper, or holds a non-zero value for it, which is what lets
// a struct literal override.
func (o *Options) merge(later *Options) *Options {
	out := o.Clone()
	if later == nil {
		return out
	}
	out.mergeImage(later)
	out.mergeSettings(later)
	out.mergeLookups(later)
	out.errs = append(out.errs, later.errs...)
	return out
}

// mergeImage overlays later's image. WithMongoImage names a whole image, so
// it replaces; everything else changes only the parts it names, so that a
// new version keeps the base's registry.
func (o *Options) mergeImage(later *Options) {
	switch {
	case later.isSet(fieldImage):
		o.WithMongoImage(later.Image)
	case !later.Image.IsZero():
		o.Image = overlay(o.Image, later.Image)
	}
	if later.isSet(fieldRegistry) {
		o.WithRegistry(later.Image.Registry)
	}
	if later.isSet(fieldRepository) {
		o.WithRepository(later.Image.Repository)
	}
	if later.isSet(fieldVersion) {
		o.WithVersion(later.Image.Version)
	}
}

// mergeSettings overlays every setting but the image.
func (o *Options) mergeSettings(later *Options) {
	if later.isSet(fieldPort) || later.Port != 0 {
		o.WithPort(later.Port)
	}
	if later.isSet(fieldName) || later.Name != "" {
		o.WithName(later.Name)
	}
	if later.isSet(fieldHostIP) || later.HostIP != "" {
		o.WithHostIP(later.HostIP)
	}
	if later.isSet(fieldReplicaSet) || later.ReplicaSet != "" {
		o.WithReplicaSet(later.ReplicaSet)
	}
	if later.choosesAuth() {
		// Together: a username from one Options and a password from another
		// would authenticate as nobody.
		o.Username, o.Password, o.NoAuth = later.Username, later.Password, later.NoAuth
		o.mark(fieldAuth)
	}
	if later.TLS {
		o.WithTLS()
	}
	if len(later.MongodArgs) > 0 {
		o.WithMongodArgs(later.MongodArgs...)
	}
	if later.isSet(fieldStartTimeout) || later.StartTimeout != 0 {
		o.WithStartTimeout(later.StartTimeout)
	}
	for key, value := range later.Labels {
		o.WithLabel(key, value)
	}
	if later.Logger != nil {
		o.WithLogger(later.Logger)
	}
	if later.Docker != nil {
		o.WithDocker(later.Docker)
	}
}

// choosesAuth reports whether these options say anything about credentials.
func (o *Options) choosesAuth() bool {
	hasCredentials := o.Username != "" || o.Password != ""
	return o.isSet(fieldAuth) || o.NoAuth || hasCredentials
}

// mergeLookups carries over any lookup a test replaced.
func (o *Options) mergeLookups(later *Options) {
	if later.getenv != nil {
		o.getenv = later.getenv
	}
	if later.detect != nil {
		o.detect = later.detect
	}
	if later.gateway != nil {
		o.gateway = later.gateway
	}
	if later.bridge != nil {
		o.bridge = later.bridge
	}
}

// generateName returns a container name random enough that parallel starts
// do not collide.
func generateName() string { return namePrefix + randomHex(nameRandomBytes) }

// randomHex returns n random bytes in hex, which needs no escaping in a
// container name, a connection string or a key file. rand.Read cannot fail
// on the Go version this module requires.
func randomHex(n int) string {
	raw := make([]byte, n)
	_, _ = rand.Read(raw)
	return hex.EncodeToString(raw)
}
