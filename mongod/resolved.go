package mongod

import (
	"os"
	"strconv"
	"time"

	"github.com/tophergopher/mongotest/dockerclient"
)

// Resolved is a set of Options with every question answered: the image worked
// out from the option, the environment and the defaults, the generated name
// filled in, and every combination checked. Start works from one of these and
// never reads an Option or the environment again, so what it creates is
// entirely determined by what Resolve returned.
//
// It is returned by Options.Resolve, which is exported so that a caller can
// see what their options actually mean without starting anything. That is the
// difference between diagnosing an image problem and guessing at one.
type Resolved struct {
	// Image is the fully determined image, with every part filled in.
	Image MongoImage
	// Port is the host port to publish, or 0 for a daemon-assigned one.
	Port int
	// Name is the container name, generated if the caller gave none.
	Name string
	// HostIP is the caller's address override, or "" to work it out.
	HostIP string
	// ReplicaSet is the replica set name, or "".
	ReplicaSet string
	// TLS reports TLS mode.
	TLS bool
	// MongodArgs are the extra mongod flags.
	MongodArgs []string
	// StartTimeout bounds the readiness wait.
	StartTimeout time.Duration
	// Labels are the container labels, including the required one.
	Labels map[string]string
	// Logger is never nil; it discards by default.
	Logger dockerclient.Logger
	// Docker is the client, or nil to build one from the environment.
	Docker dockerclient.Client

	// The lookups address resolution needs, never nil after Resolve.
	getenv  func(string) string
	detect  func() Containerisation
	gateway func() (string, error)
}

// Resolve works out the final configuration and checks it.
//
// Every setting is taken from the first of three places that has it: the
// option, then the environment variable, then the built-in default. Nothing
// here talks to a daemon or a registry, so it is cheap, deterministic, and
// safe to call in a test that asserts on what a configuration means.
//
// The checks it applies are the ones that can be made without asking anything:
// that a port is a port, that a reference is a reference, and that the options
// are ones the chosen image can actually honour. Catching those here is the
// difference between a message naming the option to change and a container
// that dies with exit code 127.
func (o *Options) Resolve() (Resolved, error) {
	merged := o.clone()
	// A helper that parses its argument could not report a failure at the
	// time, so the earliest one is reported here before anything else is
	// worked out: the rest of the configuration is not worth describing when
	// the image reference is unusable.
	if err := merged.firstError(); err != nil {
		return Resolved{}, err
	}
	getenv := merged.getenv
	if getenv == nil {
		getenv = os.Getenv
	}

	image, err := resolveImage(merged, getenv)
	if err != nil {
		return Resolved{}, err
	}
	port, err := resolvePort(merged, getenv)
	if err != nil {
		return Resolved{}, err
	}
	startTimeout, err := resolveStartTimeout(merged, getenv)
	if err != nil {
		return Resolved{}, err
	}
	name := merged.Name
	if name == "" {
		if name, err = generateName(); err != nil {
			return Resolved{}, err
		}
	}
	hostIP := merged.HostIP
	if hostIP == "" {
		hostIP = getenv(EnvHostIP)
	}

	labels := map[string]string{}
	for key, value := range merged.Labels {
		labels[key] = value
	}
	// Set last, so a caller's label map cannot drop it.
	labels[RequiredLabelKey] = RequiredLabelValue

	logger := merged.Logger
	if logger == nil {
		logger = dockerclient.NopLogger()
	}
	detect := merged.detect
	if detect == nil {
		detect = DetectContainerisation
	}
	gateway := merged.gateway
	if gateway == nil {
		gateway = realGateway
	}

	resolved := Resolved{
		Image: image, Port: port, Name: name, HostIP: hostIP,
		ReplicaSet: merged.ReplicaSet, TLS: merged.TLS,
		MongodArgs:   append([]string(nil), merged.MongodArgs...),
		StartTimeout: startTimeout, Labels: labels, Logger: logger,
		Docker: merged.Docker,
		getenv: getenv, detect: detect, gateway: gateway,
	}
	if err := resolved.validate(merged); err != nil {
		return Resolved{}, err
	}
	return resolved, nil
}

// resolveImage assembles the image from the option, the environment and the
// defaults, part by part so that each can come from a different place.
func resolveImage(o *Options, getenv func(string) string) (MongoImage, error) {
	image := MongoImage{}

	// The environment first, so that an option can override it.
	if whole := getenv(EnvImage); whole != "" {
		parsed, err := ParseMongoImage(whole)
		if err != nil {
			return MongoImage{}, environmentError(EnvImage, whole, err)
		}
		image = parsed
	}
	if registry := getenv(EnvImageRegistry); registry != "" {
		image.Registry = registry
	}
	if repository := getenv(EnvImageRepository); repository != "" {
		image.Repository = repository
	}
	if version := getenv(EnvImageVersion); version != "" {
		image.Version = version
	}

	// Then the option, which overrides whatever the environment said, part by
	// part for the same reason. WithImage has already split its reference into
	// these same parts, so there is one representation of the image here and
	// not two.
	image = overlay(image, o.Image)

	// A part set explicitly to empty is honoured where that means something
	// and refused where it does not. An empty registry is Docker Hub, which is
	// a real answer; an empty repository or version names nothing, and
	// silently substituting a default there would ignore what was asked for.
	if o.isSet(fieldRegistry) {
		image.Registry = o.Image.Registry
	}
	if o.isSet(fieldRepository) {
		if o.Image.Repository == "" {
			return MongoImage{}, ErrEmptyRepository
		}
		image.Repository = o.Image.Repository
	}
	if o.isSet(fieldVersion) {
		if o.Image.Version == "" {
			return MongoImage{}, ErrEmptyVersion
		}
		image.Version = o.Image.Version
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

// overlay returns base with every non-empty part of over applied, which is
// what lets a version be changed without restating the registry.
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

// resolvePort takes the port from the option, then the environment, then zero
// for a daemon-assigned one.
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

// resolveStartTimeout takes the budget from the option, then the environment,
// then the default.
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

// validate rejects what cannot work, before anything is created. The original
// Options is passed in so that a message can say which helper to remove.
func (r Resolved) validate(o *Options) error {
	if r.Port < 0 || r.Port > 65535 {
		return invalidPort(r.Port)
	}
	if r.StartTimeout <= 0 {
		return invalidStartTimeout(r.StartTimeout)
	}
	if o.isSet(fieldHostIP) && r.HostIP == "" {
		return ErrEmptyHostIP
	}
	if !r.Image.BareVersionTags() && isBareVersion(r.Image.Version) {
		return &NoSuchVersionError{Image: r.Image}
	}
	if !r.Image.AcceptsMongodArgs() {
		if r.ReplicaSet != "" {
			return &UnsupportedForImageError{Image: r.Image, Option: "WithReplicaSet", Reason: reasonPreconfiguredReplicaSet}
		}
		if len(r.MongodArgs) > 0 {
			return &UnsupportedForImageError{Image: r.Image, Option: "WithMongodArgs", Reason: reasonCommandIsEntrypoint}
		}
	}
	return nil
}

// isBareVersion reports whether a tag is just a version, with no OS variant
// after it. MongoDB's own server images publish none of these.
func isBareVersion(version string) bool {
	if version == "" || version == "latest" {
		return false
	}
	if version[0] < '0' || version[0] > '9' {
		return false
	}
	// An OS variant is separated by a dash: "8.0-ubi9". Anything that is only
	// digits and dots is a bare version.
	for _, c := range version {
		if (c < '0' || c > '9') && c != '.' {
			return false
		}
	}
	return true
}
