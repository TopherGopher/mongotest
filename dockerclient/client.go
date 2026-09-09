package dockerclient

import (
	"context"
	"io"
	"io/fs"
	"log/slog"
)

// Client is the Docker Engine API surface mongotest depends on. It is
// implemented by dockerapi (the standard-library client), mobyclient (the
// official client) and the doubles in dockermock.
//
// Implementations must be safe for concurrent use: mongotest drives one
// client from parallel tests.
type Client interface {
	// Host reports the daemon address the client resolved, in DOCKER_HOST
	// form ("unix:///var/run/docker.sock", "tcp://host:2375").
	Host() string

	// ImagePull fetches ref from its registry. It returns once the daemon
	// has finished, and reports a failure the daemon sends inside the pull
	// progress stream as a *PullError.
	ImagePull(ctx context.Context, ref string) error

	// ImageInspect returns metadata for a local image, by reference
	// ("mongo:8") or id ("sha256:..."). A missing image is ErrNotFound.
	ImageInspect(ctx context.Context, ref string) (ImageInspect, error)

	// ContainerCreate creates a container and returns its id along with any
	// warnings the daemon reported. name may be empty, in which case the
	// daemon generates one. When the image is not present locally the error
	// is ErrNotFound, so callers can pull and retry.
	ContainerCreate(ctx context.Context, name string, cfg ContainerConfig) (id string, warnings []string, err error)

	// ContainerStart starts a created container. Starting one that is
	// already running is not an error.
	ContainerStart(ctx context.Context, id string) error

	// ContainerRemove deletes a container. Use RemoveOptions{Force: true} for
	// one that is still running. A container that is already gone is
	// ErrNotFound.
	ContainerRemove(ctx context.Context, id string, opts RemoveOptions) error

	// ContainerInspect returns the state of a container and the host
	// addresses its ports are published on.
	ContainerInspect(ctx context.Context, id string) (ContainerInspect, error)

	// ContainerTop lists the processes running inside a container. It is the
	// only way to tell that the process a container was started for is
	// actually running: a published port is bound before that happens.
	ContainerTop(ctx context.Context, id string) (Top, error)

	// CopyToContainer writes files under destDir inside the container. It
	// works on a container that has been created but not yet started, which
	// is how material such as TLS certificates is put in place before the
	// process runs.
	CopyToContainer(ctx context.Context, id, destDir string, files []File) error

	// CopyArchiveToContainer extracts a tar stream under destDir inside the
	// container. Use it when the caller already has an archive;
	// CopyToContainer is the convenience for a handful of files.
	CopyArchiveToContainer(ctx context.Context, id, destDir string, archive io.Reader) error

	// ExecCreate registers a command to run inside a container and returns
	// the exec instance id. stdout and stderr are always captured
	// separately; no TTY is allocated.
	ExecCreate(ctx context.Context, containerID string, cfg ExecConfig) (execID string, err error)

	// ExecStartTo runs a created exec instance, writing its stdout and
	// stderr into the given writers as the process produces them. Either
	// writer may be nil to discard that stream.
	ExecStartTo(ctx context.Context, execID string, stdout, stderr io.Writer) error

	// ExecInspect reports whether an exec instance is still running and, once
	// it is not, its exit code.
	ExecInspect(ctx context.Context, execID string) (ExecInspect, error)

	// Exec runs a command inside a container, waits for it to finish, and
	// returns its output and exit code. A non-zero exit code is reported in
	// the result, not as an error, because a failing command is a normal
	// outcome the caller usually wants to inspect.
	Exec(ctx context.Context, containerID string, cmd ...string) (ExecResult, error)
}

// ContainerConfig describes a container to create. Unset fields take the
// daemon's defaults.
type ContainerConfig struct {
	// Image is the reference or id to run; required.
	Image string `json:"Image"`
	// Cmd overrides the image command. For the official mongo image the
	// entrypoint prepends "mongod", so pass flags only.
	Cmd []string `json:"Cmd,omitempty"`
	// Env holds KEY=value entries.
	Env []string `json:"Env,omitempty"`
	// Labels are attached to the container; mongotest always sets
	// mongotest=regression so stray containers can be found and removed.
	Labels map[string]string `json:"Labels,omitempty"`
	// ExposedPorts lists container ports such as "27017/tcp".
	ExposedPorts map[string]struct{} `json:"ExposedPorts,omitempty"`
	Tty          bool                `json:"Tty,omitzero"`
	OpenStdin    bool                `json:"OpenStdin,omitzero"`
	// HostConfig holds host-side settings; nil means daemon defaults.
	HostConfig *HostConfig `json:"HostConfig,omitempty"`
}

// HostConfig holds the host-side settings of a container.
type HostConfig struct {
	// PortBindings maps a container port such as "27017/tcp" to the host
	// addresses it is published on.
	PortBindings map[string][]PortBinding `json:"PortBindings,omitempty"`
	// AutoRemove asks the daemon to delete the container when it exits.
	AutoRemove bool `json:"AutoRemove,omitzero"`
}

// PortBinding is one published host address for a container port.
type PortBinding struct {
	// HostIP is the interface to publish on; "127.0.0.1" keeps it local to
	// this machine.
	HostIP string `json:"HostIp"`
	// HostPort is the host port, or "" to let the daemon choose a free one.
	// Read the chosen port back with ContainerInspect and HostPort; letting
	// the daemon choose avoids the race between finding a free port and
	// binding it that starting many containers at once otherwise hits.
	HostPort string `json:"HostPort"`
}

// RemoveOptions controls ContainerRemove.
type RemoveOptions struct {
	// Force kills a running container before removing it.
	Force bool
	// RemoveVolumes also removes anonymous volumes attached to the container.
	RemoveVolumes bool
}

// ContainerInspect is a container's state as the daemon reports it.
type ContainerInspect struct {
	ID string `json:"Id"`
	// Name is reported with a leading slash, as the daemon stores it.
	Name            string          `json:"Name"`
	State           ContainerState  `json:"State"`
	Config          InspectedConfig `json:"Config"`
	NetworkSettings NetworkSettings `json:"NetworkSettings"`
}

// ContainerState is the runtime state of a container.
type ContainerState struct {
	// Status is one of created, running, paused, restarting, removing,
	// exited or dead.
	Status   string `json:"Status"`
	Running  bool   `json:"Running"`
	ExitCode int    `json:"ExitCode"`
}

// InspectedConfig is the part of a container's configuration that inspect
// reports back.
type InspectedConfig struct {
	Image  string            `json:"Image"`
	Labels map[string]string `json:"Labels"`
}

// NetworkSettings holds the published ports of a container.
type NetworkSettings struct {
	// Ports maps a container port such as "27017/tcp" to the host addresses
	// it is published on, and is nil for ports that are not published.
	Ports map[string][]PortBinding `json:"Ports"`
}

// HostPort returns the first host port that containerPort is published on,
// or "" when it is not published. This is how a caller learns the port the
// daemon chose when PortBinding.HostPort was left empty.
func (ci ContainerInspect) HostPort(containerPort string) string {
	for _, b := range ci.NetworkSettings.Ports[containerPort] {
		if b.HostPort != "" {
			return b.HostPort
		}
	}
	return ""
}

// Top is the process listing inside a container.
type Top struct {
	Titles    []string   `json:"Titles"`
	Processes [][]string `json:"Processes"`
}

// ImageInspect is the image metadata mongotest uses.
type ImageInspect struct {
	ID           string   `json:"Id"`
	RepoTags     []string `json:"RepoTags"`
	Architecture string   `json:"Architecture"`
	OS           string   `json:"Os"`
}

// File is one regular file to place in a container with CopyToContainer.
type File struct {
	// Name is the path relative to the destination directory. It may name
	// directories ("mongo-tls/server.pem"), which are created as needed. It
	// must not be absolute, escape the destination with "..", or contain a
	// control character.
	Name string
	// Mode holds the unix permission bits (for example 0o644); 0 means
	// 0o644. Only permission bits are accepted: type bits such as fs.ModeDir
	// and the setuid, setgid and sticky bits are rejected.
	Mode    fs.FileMode
	Content []byte
}

// ExecConfig describes a command to run inside a container. stdout and
// stderr are always captured separately, so no TTY is offered.
type ExecConfig struct {
	// Cmd is the program and its arguments; required.
	Cmd []string
	// Env holds KEY=value entries. Needs Engine API 1.25 or newer.
	Env []string
	// WorkingDir is an absolute path inside the container. Needs Engine API
	// 1.35 or newer.
	WorkingDir string
}

// ExecInspect is the state of an exec instance.
type ExecInspect struct {
	ID          string `json:"ID"`
	Running     bool   `json:"Running"`
	ExitCode    int    `json:"ExitCode"`
	ContainerID string `json:"ContainerID"`
}

// ExecResult is the outcome of Exec. A non-zero ExitCode is not an error.
type ExecResult struct {
	Stdout   string
	Stderr   string
	ExitCode int
}

// Logger is the structured logging surface the clients emit to. It is
// satisfied directly by *slog.Logger, and by the adapters in the log/
// sub-modules for logrus, zap and zerolog.
type Logger interface {
	Debug(msg string, keysAndValues ...any)
	Info(msg string, keysAndValues ...any)
	Warn(msg string, keysAndValues ...any)
	Error(msg string, keysAndValues ...any)
}

var _ Logger = (*slog.Logger)(nil)

// NopLogger returns a Logger that discards everything. It is the default for
// every implementation.
func NopLogger() Logger { return slog.New(slog.DiscardHandler) }
