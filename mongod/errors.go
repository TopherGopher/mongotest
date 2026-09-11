package mongod

import (
	"errors"
	"fmt"
	"net"
	"strconv"
	"time"

	"github.com/tophergopher/mongotest/dockerclient"
)

// Every error this package returns is one of these sentinels or a typed value
// that wraps one, so callers branch with errors.Is and never parse a message.
// The types below carry the values at fault and say what to do about them.
var (
	// ErrNoPublishedPort is matched when a container port has no host address
	// to connect to: the daemon published nothing for it, or the caller asked
	// about a port that was never exposed.
	ErrNoPublishedPort = errors.New("container port is not published on the host")
	// ErrNotReady is matched when mongod did not become reachable inside the
	// start timeout. The error also wraps the context's own error, so
	// errors.Is(err, context.DeadlineExceeded) distinguishes running out of
	// time from the caller cancelling.
	ErrNotReady = errors.New("mongod did not become ready in time")
	// ErrUnresolvedHost is matched when the address a container is reachable
	// at could not be worked out. It also matches ErrInvalidArgument, because
	// what is missing is a setting only the caller can supply.
	ErrUnresolvedHost = errors.New("cannot work out the address to reach the container on")
	// ErrNoAvailablePort is matched when GetAvailablePort could not get a
	// port from the kernel.
	ErrNoAvailablePort = errors.New("no free tcp port could be found")
	// ErrContainerExited is matched when the container stopped, or was
	// removed, before mongod became reachable. It is a crash to investigate,
	// not a start to wait longer for.
	ErrContainerExited = errors.New("the container exited before mongod was ready")
)

// Predeclared errors for the option values whose message never varies, so
// that callers can compare them directly as well as with errors.Is.
var (
	// ErrEmptyImage is returned when WithImage is given an empty reference.
	ErrEmptyImage = dockerclient.InvalidArgument("image", "", "the value is empty",
		`pass a reference such as WithImage("mongo:8"), or leave the option out to get the default`)
	// ErrEmptyHostIP is returned when WithHostIP is given an empty address.
	ErrEmptyHostIP = dockerclient.InvalidArgument("host ip", "", "the value is empty",
		`pass the address containers are reachable at, such as WithHostIP("172.17.0.1"), or leave the option out to have it worked out`)
)

// invalidPort describes a host port outside the range a port can be.
func invalidPort(port int) error {
	return dockerclient.InvalidArgument("host port", strconv.Itoa(port), "the value is outside the range 1 to 65535",
		"pass a port in that range, or leave WithPort out to have the daemon assign one")
}

// invalidStartTimeout describes a start timeout that cannot bound anything.
func invalidStartTimeout(d time.Duration) error {
	return dockerclient.InvalidArgument("start timeout", d.String(), "the value is not positive",
		"pass a positive duration such as WithStartTimeout(90*time.Second), or leave the option out to get the 60 second default")
}

// StartError says which step of a start failed, and about which container. It
// wraps the underlying failure, so a caller can still branch on the daemon's
// own error with errors.Is.
type StartError struct {
	// Op names the step that failed, in the imperative: "create container".
	Op string
	// Name is the container name the start was using.
	Name string
	// Image is the image the container was to run.
	Image string
	// Err is the underlying failure.
	Err error
}

func (e *StartError) Error() string {
	return fmt.Sprintf("mongod: cannot %s %s from image %s: %v", e.Op, e.Name, e.Image, e.Err)
}

func (e *StartError) Unwrap() error { return e.Err }

// UnpublishedPortError names a container port that has no host address. The
// usual cause is a create request that did not ask for the port to be
// published, or a daemon that could not bind it.
type UnpublishedPortError struct {
	// Port is the container port that was asked about, such as "27017/tcp".
	Port string
	// Name is the container name.
	Name string
	// ID is the container id.
	ID string
}

func (e *UnpublishedPortError) Error() string {
	return fmt.Sprintf("mongod: container %s (%s) has no host address for %s: the daemon did not publish it; "+
		"check that nothing on the host is already bound to a pinned WithPort, and that the daemon has a port range free",
		e.Name, shortID(e.ID), e.Port)
}

func (e *UnpublishedPortError) Is(target error) bool { return target == ErrNoPublishedPort }

// NotReadyError reports that mongod never answered on the address it was
// published at. It wraps the reason the wait ended and, when there was one,
// the last failure the probe saw, because that is usually what explains it.
type NotReadyError struct {
	// Name is the container name.
	Name string
	// ID is the container id.
	ID string
	// Host and Port are the address the probe was dialling.
	Host string
	Port int
	// Timeout is the budget the wait was given.
	Timeout time.Duration
	// Cause is the context's error: DeadlineExceeded when the budget ran out,
	// Canceled when the caller gave up first.
	Cause error
	// LastErr is the last failure the probe saw, or nil if it never got one.
	LastErr error
}

func (e *NotReadyError) Error() string {
	msg := fmt.Sprintf("mongod: container %s (%s) was not reachable at %s within %s",
		e.Name, shortID(e.ID), addr(e.Host, e.Port), e.Timeout)
	if e.LastErr != nil {
		msg += fmt.Sprintf(": last attempt failed with %v", e.LastErr)
	}
	return msg + "; check the container's logs for why mongod exited, raise the budget with WithStartTimeout, " +
		"or correct the address with WithHostIP if the daemon publishes ports somewhere this process cannot reach"
}

func (e *NotReadyError) Unwrap() []error {
	if e.LastErr == nil {
		return []error{e.Cause}
	}
	return []error{e.Cause, e.LastErr}
}

func (e *NotReadyError) Is(target error) bool { return target == ErrNotReady }

// UnresolvedHostError reports that this process is inside a container, the
// daemon is on a unix socket, and nothing said where the published ports can
// be reached from here. It matches ErrInvalidArgument as well as
// ErrUnresolvedHost: the daemon is fine, what is missing is a setting.
type UnresolvedHostError struct {
	// DockerHost is the daemon address the client reported.
	DockerHost string
	// Signal is what decided this process is containerised, so that a wrong
	// conclusion can be argued with rather than only worked around.
	Signal string
	// Err is the underlying failure, such as having no default route.
	Err error
}

func (e *UnresolvedHostError) Error() string {
	return fmt.Sprintf("mongod: cannot work out where containers started through %s are reachable from here: "+
		"this process looks containerised (%s) and %v. "+
		"Ports published by a sibling container land on the daemon's host, which is a different network namespace from this one. "+
		"Set %s, or pass WithHostIP, to the address this process can reach that host at",
		e.DockerHost, e.Signal, e.Err, envHostIP)
}

func (e *UnresolvedHostError) Unwrap() error { return e.Err }

func (e *UnresolvedHostError) Is(target error) bool {
	return target == ErrUnresolvedHost || target == dockerclient.ErrInvalidArgument
}

// portLookupError reports that the kernel would not hand out a free port.
type portLookupError struct {
	// Err is the failure from the listen or the address read.
	Err error
}

func (e *portLookupError) Error() string {
	return fmt.Sprintf("mongod: cannot find a free tcp port on 127.0.0.1: %v; "+
		"the usual cause is a process limit on open files, or a sandbox that forbids listening", e.Err)
}

func (e *portLookupError) Unwrap() error { return e.Err }

func (e *portLookupError) Is(target error) bool { return target == ErrNoAvailablePort }

// shortID abbreviates a container id the way the docker CLI does, so a
// message stays readable. Short ids are only for people to read; every call
// to the daemon uses the full one.
func shortID(id string) string {
	const shortIDLength = 12
	if len(id) <= shortIDLength {
		return id
	}
	return id[:shortIDLength]
}

// addr renders a host and port for a message, bracketing an ipv6 address.
func addr(host string, port int) string {
	return net.JoinHostPort(host, strconv.Itoa(port))
}

// errNotATCPAddress reports a TCP listener whose address is not a TCP
// address, which the standard library does not do but the type assertion has
// to account for.
var errNotATCPAddress = errors.New("the listener's address is not a tcp address")

// nameError reports that a container name could not be generated, which means
// the system's random source is unavailable.
type nameError struct {
	// Err is the failure from the random source.
	Err error
}

func (e *nameError) Error() string {
	return fmt.Sprintf("mongod: cannot generate a container name: %v; "+
		"the name is random so that parallel starts do not collide, so pass WithName to supply one instead", e.Err)
}

func (e *nameError) Unwrap() error { return e.Err }

// ContainerExitedError reports that the container stopped, or was removed,
// before mongod ever became reachable. It is returned instead of waiting out
// the start timeout, because a container that has exited is never going to
// answer and the exit code is what explains why.
type ContainerExitedError struct {
	// Name is the container name.
	Name string
	// ID is the container id.
	ID string
	// Status is the daemon's word for the state: "exited", "dead", or
	// "removed" when the container is gone entirely.
	Status string
	// ExitCode is the process's exit code, valid when HasExitCode is set.
	ExitCode int
	// HasExitCode distinguishes an exit code of 0 from not having one, which
	// is the case for a container that was removed.
	HasExitCode bool
	// Err is the failure that prompted the check, usually the daemon refusing
	// a process listing for a container that is not running.
	Err error
}

func (e *ContainerExitedError) Error() string {
	msg := fmt.Sprintf("mongod: container %s (%s) %s before it was ready", e.Name, shortID(e.ID), e.Status)
	if e.HasExitCode {
		msg += fmt.Sprintf(" with exit code %d", e.ExitCode)
	}
	return msg + "; `docker logs " + shortID(e.ID) + "` has mongod's own explanation, and the usual causes are an unrecognised " +
		"argument passed through WithMongodArgs, a replica set name mongod rejects, or too little memory for the storage engine"
}

func (e *ContainerExitedError) Unwrap() error { return e.Err }

func (e *ContainerExitedError) Is(target error) bool { return target == ErrContainerExited }
