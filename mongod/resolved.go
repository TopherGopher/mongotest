package mongod

import (
	"maps"
	"os"
	"strconv"
	"time"

	"github.com/tophergopher/mongotest/dockerclient"
)

// Resolved is a set of Options with every question answered: defaults filled
// in, the environment read, and the combination checked. Start works only
// from this. Call [Options.Resolve] to see what a configuration means
// without starting anything.
type Resolved struct {
	// Image is the image to run, with every part filled in.
	Image MongoImage
	// Port is the host port to publish, or 0 for one the daemon chooses.
	Port int
	// Name is the container name, generated if none was given.
	Name string
	// HostIP is the WithHostIP override, or "" to work the address out.
	HostIP string
	// ReplicaSet is the replica set name, or "".
	ReplicaSet string
	// Username and Password are the root user to create, or "". Start
	// generates a pair if these are empty and the port is published beyond
	// loopback, which is not known until the daemon is.
	Username string
	Password string
	// NoAuth says never to generate credentials.
	NoAuth bool
	// TLS reports TLS mode.
	TLS bool
	// MongodArgs are the extra mongod flags.
	MongodArgs []string
	// StartTimeout is how long to wait for mongod.
	StartTimeout time.Duration
	// Labels are the container labels, including the required one.
	Labels map[string]string
	// Logger is never nil; it discards by default.
	Logger dockerclient.Logger
	// Docker is the client, or nil to find the daemon from the environment.
	Docker dockerclient.Client

	// The lookups address resolution uses; never nil.
	getenv  func(string) string
	detect  func() Containerisation
	gateway func() (string, error)
	bridge  func() (string, bool)
}

// Resolve works out the final configuration and checks it. Each setting
// comes from the option, then the environment variable, then the default.
// It contacts nothing, so it is safe to call from a test.
func (o *Options) Resolve() (Resolved, error) {
	merged := o.Clone()
	if len(merged.errs) > 0 {
		return Resolved{}, merged.errs[0]
	}
	// WithHostIP("") is a mistake, not a request to use the environment.
	if merged.isSet(fieldHostIP) && merged.HostIP == "" {
		return Resolved{}, ErrEmptyHostIP
	}
	resolved := merged.withDefaults()

	var err error
	if resolved.Image, err = resolveImage(merged, resolved.getenv); err != nil {
		return Resolved{}, err
	}
	if resolved.Port, err = resolvePort(merged, resolved.getenv); err != nil {
		return Resolved{}, err
	}
	if resolved.StartTimeout, err = resolveStartTimeout(merged, resolved.getenv); err != nil {
		return Resolved{}, err
	}
	if err := resolved.validate(); err != nil {
		return Resolved{}, err
	}
	return resolved, nil
}

// withDefaults copies the settings that need no parsing, filling in a name,
// the required label, a logger and the real lookups where none was given.
func (o *Options) withDefaults() Resolved {
	r := Resolved{
		Name: o.Name, HostIP: o.HostIP, ReplicaSet: o.ReplicaSet,
		Username: o.Username, Password: o.Password, NoAuth: o.NoAuth, TLS: o.TLS,
		MongodArgs: append([]string(nil), o.MongodArgs...),
		Labels:     maps.Clone(o.Labels),
		Logger:     o.Logger, Docker: o.Docker,
		getenv: o.getenv, detect: o.detect, gateway: o.gateway, bridge: o.bridge,
	}
	if r.Name == "" {
		r.Name = generateName()
	}
	if r.Labels == nil {
		r.Labels = map[string]string{}
	}
	r.Labels[RequiredLabelKey] = RequiredLabelValue
	if r.Logger == nil {
		r.Logger = dockerclient.NopLogger()
	}
	if r.getenv == nil {
		r.getenv = os.Getenv
	}
	if r.detect == nil {
		r.detect = DetectContainerisation
	}
	if r.gateway == nil {
		r.gateway = realGateway
	}
	if r.bridge == nil {
		r.bridge = realBridge
	}
	return r
}

// resolveImage builds the image part by part: the environment first, the
// option over it, then the defaults for whatever is still empty.
func resolveImage(o *Options, getenv func(string) string) (MongoImage, error) {
	image, err := imageFromEnv(getenv)
	if err != nil {
		return MongoImage{}, err
	}
	image = overlay(image, o.Image)

	// A helper can set a part to empty on purpose. An empty registry means
	// Docker Hub; an empty repository or version names nothing.
	if o.isSet(fieldRegistry) {
		image.Registry = o.Image.Registry
	}
	if o.isSet(fieldRepository) && o.Image.Repository == "" {
		return MongoImage{}, ErrEmptyRepository
	}
	if o.isSet(fieldVersion) && o.Image.Version == "" {
		return MongoImage{}, ErrEmptyVersion
	}

	if image.Repository == "" {
		image.Repository = RepositoryOfficial
	}
	if image.Version == "" {
		image.Version = defaultVersionFor(image.Repository)
	}
	if _, err := dockerclient.ParseImageRef(image.Reference()); err != nil {
		return MongoImage{}, err
	}
	return image, nil
}

// imageFromEnv reads the image the environment asks for. The per-part
// variables override the matching part of EnvImage.
func imageFromEnv(getenv func(string) string) (MongoImage, error) {
	var image MongoImage
	if whole := getenv(EnvImage); whole != "" {
		parsed, err := ParseMongoImage(whole)
		if err != nil {
			return MongoImage{}, environmentError(EnvImage, whole, err)
		}
		image = parsed
	}
	parts := MongoImage{
		Registry:   getenv(EnvImageRegistry),
		Repository: getenv(EnvImageRepository),
		Version:    getenv(EnvImageVersion),
	}
	return overlay(image, parts), nil
}

// overlay returns base with every non-empty part of over applied.
func overlay(base, over MongoImage) MongoImage {
	if over.Registry != "" {
		base.Registry = over.Registry
	}
	if over.Repository != "" {
		base.Repository = over.Repository
	}
	if over.Version != "" {
		base.Version = over.Version
	}
	return base
}

// resolvePort takes the port from the option, then the environment, then
// zero, which lets the daemon choose.
func resolvePort(o *Options, getenv func(string) string) (int, error) {
	if o.isSet(fieldPort) || o.Port != 0 {
		return o.Port, nil
	}
	raw := getenv(EnvPort)
	if raw == "" {
		return 0, nil
	}
	port, err := strconv.Atoi(raw)
	if err != nil {
		return 0, environmentError(EnvPort, raw, errNotANumber)
	}
	return port, nil
}

// resolveStartTimeout takes the timeout from the option, then the
// environment, then the default.
func resolveStartTimeout(o *Options, getenv func(string) string) (time.Duration, error) {
	if o.isSet(fieldStartTimeout) || o.StartTimeout != 0 {
		return o.StartTimeout, nil
	}
	raw := getenv(EnvStartTimeout)
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
func (r Resolved) validate() error {
	hasUsername, hasPassword := r.Username != "", r.Password != ""
	switch {
	case r.Port < 0 || r.Port > 65535:
		return invalidPort(r.Port)
	case r.StartTimeout <= 0:
		return invalidStartTimeout(r.StartTimeout)
	case hasUsername != hasPassword:
		return ErrIncompleteCredentials
	case r.NoAuth && hasUsername:
		return ErrConflictingAuth
	case !r.Image.BareVersionTags() && isBareVersion(r.Image.Version):
		return &NoSuchVersionError{Image: r.Image}
	case r.ReplicaSet != "" && r.Image.ReplicaSetPreconfigured():
		return &UnsupportedForImageError{Image: r.Image, Option: "WithReplicaSet", Reason: reasonPreconfiguredReplicaSet}
	case len(r.MongodArgs) > 0 && !r.Image.AcceptsMongodArgs():
		return &UnsupportedForImageError{Image: r.Image, Option: "WithMongodArgs", Reason: reasonCommandIsEntrypoint}
	}
	return nil
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
