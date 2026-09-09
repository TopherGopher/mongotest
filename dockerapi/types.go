package dockerapi

import "github.com/tophergopher/mongotest/dockerclient"

// The request and response types, the Logger and every error this package
// reports are declared by dockerclient, so that a caller can swap this
// client for mobyclient or one of the doubles in dockermock without
// changing a line. They are aliased here so existing code that names them
// through this package keeps compiling, and so a reader following
// dockerapi's own documentation is not sent to another package for the
// shape of a request.
type (
	// ContainerConfig describes a container to create.
	ContainerConfig = dockerclient.ContainerConfig
	// HostConfig holds the host-side settings of a container.
	HostConfig = dockerclient.HostConfig
	// PortBinding is one published host address for a container port.
	PortBinding = dockerclient.PortBinding
	// RemoveOptions controls ContainerRemove.
	RemoveOptions = dockerclient.RemoveOptions
	// ContainerInspect is a container's state as the daemon reports it.
	ContainerInspect = dockerclient.ContainerInspect
	// ContainerState is the runtime state of a container.
	ContainerState = dockerclient.ContainerState
	// InspectedConfig is the configuration inspect reports back.
	InspectedConfig = dockerclient.InspectedConfig
	// NetworkSettings holds the published ports of a container.
	NetworkSettings = dockerclient.NetworkSettings
	// Top is the process listing inside a container.
	Top = dockerclient.Top
	// ImageInspect is the image metadata mongotest uses.
	ImageInspect = dockerclient.ImageInspect
	// File is one regular file to place in a container.
	File = dockerclient.File
	// ExecConfig describes a command to run inside a container.
	ExecConfig = dockerclient.ExecConfig
	// ExecInspect is the state of an exec instance.
	ExecInspect = dockerclient.ExecInspect
	// ExecResult is the outcome of Exec.
	ExecResult = dockerclient.ExecResult
	// Logger is the structured logging surface this client emits to.
	Logger = dockerclient.Logger

	// InvalidArgumentError describes a value refused before any request.
	InvalidArgumentError = dockerclient.InvalidArgumentError
	// StatusError is a non-2xx response from the daemon.
	StatusError = dockerclient.StatusError
	// ResponseError is a reply the client could not interpret.
	ResponseError = dockerclient.ResponseError
	// ConnectionError is a failure to reach the daemon at all.
	ConnectionError = dockerclient.ConnectionError
	// APIVersionError is a version mismatch with the daemon.
	APIVersionError = dockerclient.APIVersionError
	// StreamError is a malformed exec stream, or an error inside one.
	StreamError = dockerclient.StreamError
	// PullError is a failure reported inside a pull progress stream.
	PullError = dockerclient.PullError
)

// The error sentinels callers branch on. Each is the same value dockerclient
// declares, so errors.Is behaves identically whichever client produced the
// error.
var (
	ErrNotFound         = dockerclient.ErrNotFound
	ErrConflict         = dockerclient.ErrConflict
	ErrUnauthorized     = dockerclient.ErrUnauthorized
	ErrConnectionFailed = dockerclient.ErrConnectionFailed
	ErrInvalidArgument  = dockerclient.ErrInvalidArgument
	ErrAPIVersion       = dockerclient.ErrAPIVersion
	ErrDaemonResponse   = dockerclient.ErrDaemonResponse
	ErrStream           = dockerclient.ErrStream
	ErrPull             = dockerclient.ErrPull
	ErrExecUnfinished   = dockerclient.ErrExecUnfinished

	// ErrNoCommand is returned by Exec and ExecCreate with no program.
	ErrNoCommand = dockerclient.ErrNoCommand
	// ErrNoFiles is returned by CopyToContainer with no files.
	ErrNoFiles = dockerclient.ErrNoFiles
	// ErrEmptyHost is returned when the docker host string is empty.
	ErrEmptyHost = dockerclient.ErrEmptyHost
)

// IsNotFound reports whether err is a 404 from the daemon.
func IsNotFound(err error) bool { return dockerclient.IsNotFound(err) }

// NopLogger returns a Logger that discards everything; it is the default.
func NopLogger() Logger { return dockerclient.NopLogger() }

// invalidArg builds the shared InvalidArgumentError.
func invalidArg(argument, value, problem, fix string) *InvalidArgumentError {
	return dockerclient.InvalidArgument(argument, value, problem, fix)
}

// decodeError reports that a daemon response could not be decoded.
func decodeError(method, path string, err error) *ResponseError {
	return dockerclient.DecodeError(method, path, err)
}

// unexpectedStatus reports a success status the endpoint does not document.
func unexpectedStatus(method, path string, code int) *ResponseError {
	return dockerclient.UnexpectedStatus(method, path, code)
}

// wrapConnError turns a transport failure into an actionable
// ConnectionError; see dockerclient.WrapConnectionError.
func wrapConnError(host string, err error) error {
	return dockerclient.WrapConnectionError(host, err)
}

// Client satisfies the interface every mongotest package depends on.
var _ dockerclient.Client = (*Client)(nil)
