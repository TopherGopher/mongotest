package mongod

import (
	"crypto/rand"
	"encoding/hex"
	"os"
	"strings"
	"time"

	"github.com/tophergopher/mongotest/dockerclient"
)

// Environment variables that configure a whole suite without editing code.
// An option overrides each one; each one overrides the default.
const (
	// EnvImage is the image to run, in any shape WithImage accepts.
	EnvImage = "MONGOTEST_IMAGE"
	// EnvStartTimeout is how long to wait for mongod, as a Go duration: "90s".
	EnvStartTimeout = "MONGOTEST_START_TIMEOUT"
	// EnvHostIP is the address containers are reachable at; see WithHostIP.
	EnvHostIP = "MONGOTEST_HOST_IP"
)

const (
	// DefaultStartTimeout is how long Start waits for mongod.
	DefaultStartTimeout = 60 * time.Second
	// DefaultUsername is the root user when credentials are generated.
	DefaultUsername = "mongotest"
	// RequiredLabelKey and RequiredLabelValue mark every container this
	// package starts, so a stray one can be found and removed:
	//
	//	docker rm -f $(docker ps -aq --filter label=mongotest=regression)
	RequiredLabelKey   = "mongotest"
	RequiredLabelValue = "regression"

	// mongoPort is the port mongod listens on inside the container.
	mongoPort = "27017/tcp"
	// cleanupTimeout bounds removing the container a failed start created.
	cleanupTimeout = 30 * time.Second
	// namePrefix and nameRandomBytes shape a generated container name.
	namePrefix      = "mongotest-"
	nameRandomBytes = 4
	// secretRandomBytes sizes a generated password and replica set key.
	secretRandomBytes = 16
)

// Option changes how Start runs a container. Options apply in the order
// given, so a later one overrides an earlier one:
//
//	mongod.Start(ctx, mongod.WithImage("8.0"), mongod.WithReplicaSet("rs0"))
type Option func(*config)

// config is what a start works from: the options applied over the defaults.
type config struct {
	image        MongoImage
	port         int
	name         string
	hostIP       string
	replicaSet   string
	username     string
	password     string
	noAuth       bool
	tls          bool
	mongodArgs   []string
	startTimeout time.Duration
	labels       map[string]string
	logger       dockerclient.Logger
	docker       dockerclient.Client
	// errs holds what an option could not accept. An option has nowhere to
	// return an error, so Start reports the first one.
	errs []error
}

// WithImage sets the image from a reference. Only the parts it names change:
//
//	"8.0"                                    the version
//	"mongodb/mongodb-atlas-local"            the image, at its default version
//	"public.ecr.aws/docker/library/mongo:8"  everything
func WithImage(ref string) Option {
	return func(c *config) {
		parsed, err := ParseMongoImage(ref)
		if err != nil {
			c.errs = append(c.errs, err)
			return
		}
		if parsed.Repository != "" {
			c.image.Registry, c.image.Repository = parsed.Registry, parsed.Repository
		}
		if parsed.Version != "" {
			c.image.Version = parsed.Version
		}
	}
}

// WithMongoImage sets the whole image, usually one of the known ones:
//
//	mongod.WithMongoImage(mongod.ImageAtlasLocal)
//	mongod.WithMongoImage(mongod.ImageDockerHub.WithRegistry("mirror.corp.example"))
func WithMongoImage(img MongoImage) Option {
	return func(c *config) { c.image = img }
}

// WithPort publishes mongod on a fixed host port. Prefer the default, where
// the daemon chooses: a port found free can be taken before the container
// binds it, which parallel tests hit regularly.
func WithPort(port int) Option {
	return func(c *config) { c.port = port }
}

// WithName names the container instead of generating a name.
func WithName(name string) Option {
	return func(c *config) { c.name = name }
}

// WithHostIP states the address or hostname containers are reachable at, for
// a network the detection gets wrong. Without it Start uses the first of:
//
//  1. the MONGOTEST_HOST_IP environment variable;
//  2. the daemon's own host, when the daemon is remote (tcp, http, https, ssh);
//  3. loopback, when this process shares the daemon host's network;
//  4. the default gateway, when this process is in a container with a network
//     of its own, because the port is published on the host outside it.
func WithHostIP(host string) Option {
	return func(c *config) {
		if host == "" {
			c.errs = append(c.errs, ErrEmptyHostIP)
		}
		c.hostIP = host
	}
}

// WithReplicaSet starts mongod as a single-member replica set with this
// name, which is what transactions and change streams need. Start initiates
// the set and returns once it accepts writes. That uses the image's own
// mongosh, so an image without it fails with [ErrNotInitiated].
func WithReplicaSet(name string) Option {
	return func(c *config) { c.replicaSet = name }
}

// WithAuth creates a root user and makes mongod require authentication.
// [Container.URI] then includes the credentials.
//
// Creating the user makes a start a few seconds slower, so by default
// credentials are only generated for a container other machines can reach.
// Anyone who can use the Docker daemon can read them, so do not reuse a
// password that matters.
func WithAuth(username, password string) Option {
	return func(c *config) { c.username, c.password, c.noAuth = username, password, false }
}

// WithoutAuth runs mongod without authentication even where other machines
// can reach it. Use it only on a network you trust.
func WithoutAuth() Option {
	return func(c *config) { c.username, c.password, c.noAuth = "", "", true }
}

// WithTLS adds tls=true to the URI. Certificates are not set up yet, so a
// driver cannot connect to a container started with it.
func WithTLS() Option {
	return func(c *config) { c.tls = true }
}

// WithMongodArgs passes extra flags to mongod.
func WithMongodArgs(args ...string) Option {
	return func(c *config) { c.mongodArgs = append(c.mongodArgs, args...) }
}

// WithStartTimeout sets how long Start waits for mongod.
func WithStartTimeout(d time.Duration) Option {
	return func(c *config) { c.startTimeout = d }
}

// WithLabel adds a label to the container.
func WithLabel(key, value string) Option {
	return func(c *config) { c.labels[key] = value }
}

// WithLogger sends what a start does to a logger. *slog.Logger works.
func WithLogger(l dockerclient.Logger) Option {
	return func(c *config) { c.logger = l }
}

// WithDocker uses a specific Docker client, such as a double from dockermock.
func WithDocker(client dockerclient.Client) Option {
	return func(c *config) { c.docker = client }
}

// newConfig applies the options over the defaults, fills in what was left
// open from the environment, and checks the result. It contacts nothing.
func newConfig(opts []Option) (config, error) {
	c := config{
		labels: map[string]string{},
		logger: dockerclient.NopLogger(),
	}
	for _, opt := range opts {
		if opt != nil {
			opt(&c)
		}
	}
	if len(c.errs) > 0 {
		return config{}, c.errs[0]
	}
	if c.name == "" {
		c.name = namePrefix + randomHex(nameRandomBytes)
	}
	c.labels[RequiredLabelKey] = RequiredLabelValue

	var err error
	if c.image, err = resolveImage(c.image); err != nil {
		return config{}, err
	}
	if c.startTimeout, err = resolveStartTimeout(c.startTimeout); err != nil {
		return config{}, err
	}
	return c, c.validate()
}

// resolveImage fills in what the options left open. An option that names a
// repository decides the image. Otherwise MONGOTEST_IMAGE does, with any
// version the options gave, and after that the defaults.
func resolveImage(image MongoImage) (MongoImage, error) {
	if image.Repository == "" {
		fromEnv, err := imageFromEnv()
		if err != nil {
			return MongoImage{}, err
		}
		image.Registry, image.Repository = fromEnv.Registry, fromEnv.Repository
		if image.Version == "" {
			image.Version = fromEnv.Version
		}
	}
	if image.Repository == "" {
		image.Repository = repositoryOfficial
	}
	if image.Version == "" {
		image.Version = image.defaultTag()
	}
	if _, err := dockerclient.ParseImageRef(image.Reference()); err != nil {
		return MongoImage{}, err
	}
	return image, nil
}

// imageFromEnv reads MONGOTEST_IMAGE, or returns no image if it is unset.
func imageFromEnv() (MongoImage, error) {
	ref := os.Getenv(EnvImage)
	if ref == "" {
		return MongoImage{}, nil
	}
	image, err := ParseMongoImage(ref)
	if err != nil {
		return MongoImage{}, environmentError(EnvImage, ref, err)
	}
	return image, nil
}

// resolveStartTimeout takes the timeout from the option, then
// MONGOTEST_START_TIMEOUT, then the default.
func resolveStartTimeout(fromOption time.Duration) (time.Duration, error) {
	if fromOption != 0 {
		return fromOption, nil
	}
	raw := os.Getenv(EnvStartTimeout)
	if raw == "" {
		return DefaultStartTimeout, nil
	}
	d, err := time.ParseDuration(raw)
	if err != nil {
		return 0, environmentError(EnvStartTimeout, raw, errNotADuration)
	}
	return d, nil
}

// validate rejects a configuration that cannot work, before anything is
// created, so the error can name the option to change.
func (c config) validate() error {
	hasUsername, hasPassword := c.username != "", c.password != ""
	switch {
	case c.port < 0 || c.port > 65535:
		return invalidPort(c.port)
	case c.startTimeout < 0:
		return invalidStartTimeout(c.startTimeout)
	case hasUsername != hasPassword:
		return ErrIncompleteCredentials
	case !creatable(c.username) || !creatable(c.password):
		return ErrUnusableCredentials
	case !c.image.hasBareVersionTags() && isBareVersion(c.image.Version):
		return &NoSuchVersionError{Image: c.image}
	case c.replicaSet != "" && c.image.ReplicaSetPreconfigured():
		return &UnsupportedForImageError{Image: c.image, Option: "WithReplicaSet", Reason: reasonPreconfiguredReplicaSet}
	case len(c.mongodArgs) > 0 && !c.image.acceptsMongodArgs():
		return &UnsupportedForImageError{Image: c.image, Option: "WithMongodArgs", Reason: reasonCommandIsEntrypoint}
	}
	return nil
}

// creatable reports whether every image can create a user from a username
// or password. See ErrUnusableCredentials.
func creatable(credential string) bool {
	return !strings.ContainsAny(credential, "\\`") && !strings.Contains(credential, "${")
}

// isBareVersion reports whether a tag is only digits and dots, such as "8"
// or "8.0.30", with no OS variant after it.
func isBareVersion(version string) bool {
	if version == "" {
		return false
	}
	for _, c := range version {
		if !isDigit(c) && c != '.' {
			return false
		}
	}
	return isDigit(rune(version[0]))
}

// isDigit reports whether c is an ASCII digit.
func isDigit(c rune) bool { return c >= '0' && c <= '9' }

// randomHex returns n random bytes in hex, which needs no escaping in a
// container name, a connection string or a key file. rand.Read cannot fail
// on the Go version this module requires.
func randomHex(n int) string {
	raw := make([]byte, n)
	_, _ = rand.Read(raw)
	return hex.EncodeToString(raw)
}
