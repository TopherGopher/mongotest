package mongod

import (
	"errors"
	"fmt"
	"net"
	"strconv"
	"time"

	"github.com/tophergopher/mongotest/dockerclient"
)

// Sentinels to match with errors.Is. The typed errors below carry the
// details and say what to do.
var (
	// ErrNoPublishedPort: the container port has no host port to connect to.
	ErrNoPublishedPort = errors.New("container port is not published on the host")
	// ErrNotReady: mongod was not reachable within the start timeout. The
	// error also matches context.DeadlineExceeded or context.Canceled.
	ErrNotReady = errors.New("mongod did not become ready in time")
	// ErrLoopbackOnly: when the wait ended, the only mongod running was
	// listening on the container's own loopback, where no published port
	// reaches. It accompanies ErrNotReady.
	ErrLoopbackOnly = errors.New("mongod is listening only on the container's own loopback, " +
		"which a published port cannot reach; it needs --bind_ip_all, which the image's entrypoint normally adds")
	// ErrUnresolvedHost: the address to reach the container on could not be
	// worked out. Set WithHostIP.
	ErrUnresolvedHost = errors.New("cannot work out the address to reach the container on")
	// ErrNoAvailablePort: GetAvailablePort could not get a port.
	ErrNoAvailablePort = errors.New("no free tcp port could be found")
	// ErrContainerExited: the container stopped or was removed before mongod
	// was ready, so waiting longer would not have helped.
	ErrContainerExited = errors.New("the container exited before mongod was ready")
	// ErrNoSuchVersion: the image publishes no such tag.
	ErrNoSuchVersion = errors.New("that version is not published for this image")
	// ErrAuthNotEnforced: the container was created with credentials but its
	// mongod does not ask for them. The container is removed, not returned.
	ErrAuthNotEnforced = errors.New("mongod is not enforcing the credentials it was created with")
	// ErrUnsupportedForImage: the chosen image cannot honour an option.
	ErrUnsupportedForImage = errors.New("the chosen image does not support that option")
)

// Errors for option values that are wrong in only one way. Each also matches
// dockerclient.ErrInvalidArgument.
var (
	// ErrEmptyImage: WithImage was given an empty reference.
	ErrEmptyImage = dockerclient.InvalidArgument("image", "", "the value is empty",
		`pass a reference such as WithImage("mongo:8"), or leave the option out to get the default`)
	// ErrEmptyRepository: the image's repository was set to "".
	ErrEmptyRepository = dockerclient.InvalidArgument("image repository", "", "the value is empty",
		`pass a repository such as "mongo", or leave it unset to get the default`)
	// ErrEmptyVersion: the image's version was set to "".
	ErrEmptyVersion = dockerclient.InvalidArgument("image version", "", "the value is empty",
		`pass a version such as "8" or "8.0-ubi9", or leave it unset to get the default`)
	// ErrIncompleteCredentials: only one of username and password was set.
	ErrIncompleteCredentials = dockerclient.InvalidArgument("credentials", "", "only one of the username and the password is set",
		`pass both, as in WithAuth("admin", "secret"), or neither`)
	// ErrConflictingAuth: NoAuth was set together with credentials.
	ErrConflictingAuth = dockerclient.InvalidArgument("credentials", "", "NoAuth is set together with a username and password",
		"keep the credentials or NoAuth, not both")
	// ErrEmptyHostIP: WithHostIP was given an empty address.
	ErrEmptyHostIP = dockerclient.InvalidArgument("host ip", "", "the value is empty",
		`pass an address such as WithHostIP("172.17.0.1"), or leave the option out to have it worked out`)
)

// The reasons an environment variable's value is refused.
var (
	errNotANumber   = errors.New("it is not a number")
	errNotADuration = errors.New(`it is not a duration; use a value such as "90s" or "2m"`)
)

// errNotATCPAddress guards a type assertion the standard library never fails.
var errNotATCPAddress = errors.New("the listener's address is not a tcp address")

// Why an image cannot honour an option, and what to do instead.
const (
	reasonPreconfiguredReplicaSet = "it already runs a single-node replica set under a name it generates, " +
		"so remove the option and read the name from the server"
	reasonCommandIsEntrypoint = "its command is its entrypoint, so mongod flags would replace the program " +
		"and the container would exit with code 127"
)

func invalidPort(port int) error {
	return dockerclient.InvalidArgument("host port", strconv.Itoa(port), "the value is outside the range 0 to 65535",
		"pass a port in that range; 0, or leaving WithPort out, has the daemon assign one")
}

func invalidStartTimeout(d time.Duration) error {
	return dockerclient.InvalidArgument("start timeout", d.String(), "the value is not positive",
		"pass a positive duration such as WithStartTimeout(90*time.Second), or leave the option out")
}

// environmentError reports an unusable value in an environment variable.
func environmentError(name, value string, cause error) error {
	return dockerclient.InvalidArgument(name, value, cause.Error(),
		"correct the value of the "+name+" environment variable, or unset it to use the default")
}

// The steps of a start, as StartError.Op names them.
const (
	OpPullImage   = "pull the image for"
	OpCreate      = "create container"
	OpCopyKeyFile = "copy the replica set key file into container"
	OpStart       = "start container"
	OpInspect     = "inspect container"
)

// StartError says which step of a start failed. It wraps the daemon's error.
type StartError struct {
	Op    string // one of the Op constants
	Name  string // the container name
	Image string // the image reference
	Err   error
}

func (e *StartError) Error() string {
	return fmt.Sprintf("mongod: cannot %s %s from image %s: %v", e.Op, e.Name, e.Image, e.Err)
}

func (e *StartError) Unwrap() error { return e.Err }

// UnpublishedPortError names a container port with no host port. It matches
// ErrNoPublishedPort.
type UnpublishedPortError struct {
	Port string // the container port, such as "27017/tcp"
	Name string
	ID   string
}

func (e *UnpublishedPortError) Error() string {
	return fmt.Sprintf("mongod: container %s (%s) has no host port for %s: the daemon did not publish it",
		e.Name, shortID(e.ID), e.Port)
}

func (e *UnpublishedPortError) Is(target error) bool { return target == ErrNoPublishedPort }

// NotReadyError reports that mongod was not reachable in time. It matches
// ErrNotReady, the context error that ended the wait, and the last failure
// the wait saw.
type NotReadyError struct {
	Name string
	ID   string
	// Host and Port are the address that was dialled. HostSource says what
	// chose the host, since a wrong address looks just like a slow start.
	Host       string
	Port       int
	HostSource string
	Timeout    time.Duration
	// Cause is the context's error. LastErr is the last failed attempt, or nil.
	Cause   error
	LastErr error
}

func (e *NotReadyError) Error() string {
	msg := fmt.Sprintf("mongod: container %s (%s) was not reachable at %s within %s",
		e.Name, shortID(e.ID), addr(e.Host, e.Port), e.Timeout)
	if e.LastErr != nil {
		msg += fmt.Sprintf(": last attempt failed with %v", e.LastErr)
	}
	if e.HostSource != "" {
		msg += fmt.Sprintf("; that address came from %s", e.HostSource)
	}
	return msg + "; allow longer with WithStartTimeout, or correct the address with WithHostIP or " + EnvHostIP
}

func (e *NotReadyError) Unwrap() []error {
	if e.LastErr == nil {
		return []error{e.Cause}
	}
	return []error{e.Cause, e.LastErr}
}

func (e *NotReadyError) Is(target error) bool { return target == ErrNotReady }

// UnresolvedHostError reports that this process is in a container and
// nothing says where the daemon's host is from here. It matches
// ErrUnresolvedHost and dockerclient.ErrInvalidArgument.
type UnresolvedHostError struct {
	DockerHost string // the daemon address the client reported
	Evidence   string // what showed this process is in a container
	Err        error
}

func (e *UnresolvedHostError) Error() string {
	return fmt.Sprintf("mongod: cannot work out where containers started through %s are reachable from here: "+
		"this process looks containerised (%s) and %v. Set %s, or pass WithHostIP, to the address of the daemon's host",
		e.DockerHost, e.Evidence, e.Err, EnvHostIP)
}

func (e *UnresolvedHostError) Unwrap() error { return e.Err }

func (e *UnresolvedHostError) Is(target error) bool {
	return target == ErrUnresolvedHost || target == dockerclient.ErrInvalidArgument
}

// portLookupError reports that no free port could be found. It matches
// ErrNoAvailablePort.
type portLookupError struct{ Err error }

func (e *portLookupError) Error() string {
	return fmt.Sprintf("mongod: cannot find a free tcp port on 127.0.0.1: %v", e.Err)
}

func (e *portLookupError) Unwrap() error { return e.Err }

func (e *portLookupError) Is(target error) bool { return target == ErrNoAvailablePort }

// NoSuchVersionError reports a version the image's repository cannot have.
// It matches ErrNoSuchVersion and dockerclient.ErrInvalidArgument.
type NoSuchVersionError struct {
	Image MongoImage // as resolved, including the version at fault
}

func (e *NoSuchVersionError) Error() string {
	return fmt.Sprintf("mongod: %s publishes no tag %q: every tag there names an OS variant. Pass a tag such as %q",
		e.Image.Repository, e.Image.Version, e.Image.versionHint())
}

func (e *NoSuchVersionError) Is(target error) bool {
	return target == ErrNoSuchVersion || target == dockerclient.ErrInvalidArgument
}

// UnsupportedForImageError reports an option the image cannot honour. It
// matches ErrUnsupportedForImage and dockerclient.ErrInvalidArgument.
type UnsupportedForImageError struct {
	Image  MongoImage
	Option string // the helper to remove, such as "WithReplicaSet"
	Reason string
}

func (e *UnsupportedForImageError) Error() string {
	return fmt.Sprintf("mongod: %s does not support %s: %s", e.Image.Repository, e.Option, e.Reason)
}

func (e *UnsupportedForImageError) Is(target error) bool {
	return target == ErrUnsupportedForImage || target == dockerclient.ErrInvalidArgument
}

// AuthNotEnforcedError reports a mongod running without the authentication
// its container was created to have. It matches ErrAuthNotEnforced.
type AuthNotEnforcedError struct {
	Name string
	ID   string
}

func (e *AuthNotEnforcedError) Error() string {
	return fmt.Sprintf("mongod: container %s (%s) was created with credentials, but its mongod runs without --auth: "+
		"the image did not create a root user from its environment. "+
		"Use an image that does, or pass WithoutAuth to accept a mongod anyone can use",
		e.Name, shortID(e.ID))
}

func (e *AuthNotEnforcedError) Is(target error) bool { return target == ErrAuthNotEnforced }

// ContainerExitedError reports that the container stopped, or was removed,
// before mongod was ready. It matches ErrContainerExited.
type ContainerExitedError struct {
	Name string
	ID   string
	// Status is "exited", "dead", or "removed".
	Status string
	// ExitCode is meaningful only when HasExitCode is true.
	ExitCode    int
	HasExitCode bool
	Err         error
}

func (e *ContainerExitedError) Error() string {
	msg := fmt.Sprintf("mongod: container %s (%s) %s before it was ready", e.Name, shortID(e.ID), e.Status)
	if e.HasExitCode {
		msg += fmt.Sprintf(" with exit code %d", e.ExitCode)
	}
	return msg + "; the usual cause is a flag or replica set name mongod rejects. " +
		"The container and its log have been removed, so run the image by hand with the same flags to see why"
}

func (e *ContainerExitedError) Unwrap() error { return e.Err }

func (e *ContainerExitedError) Is(target error) bool { return target == ErrContainerExited }

// shortID abbreviates a container id for a message, as the docker CLI does.
func shortID(id string) string {
	const length = 12
	if len(id) <= length {
		return id
	}
	return id[:length]
}

// addr joins a host and port, bracketing an IPv6 address.
func addr(host string, port int) string {
	return net.JoinHostPort(host, strconv.Itoa(port))
}
