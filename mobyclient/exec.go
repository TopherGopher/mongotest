package mobyclient

import (
	"context"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/moby/moby/api/pkg/stdcopy"
	"github.com/moby/moby/client"

	"github.com/tophergopher/mongotest/dockerclient"
)

// ExecCreate registers a command to run inside a container and returns the
// exec instance id. stdout and stderr are always captured separately; no
// TTY is allocated.
func (c *Client) ExecCreate(ctx context.Context, containerID string, cfg dockerclient.ExecConfig) (string, error) {
	containerID, err := dockerclient.CheckID("container", containerID)
	if err != nil {
		return "", err
	}
	if err := cfg.Validate(c.negotiatedVersion(ctx, cfg)); err != nil {
		return "", err
	}
	opts := client.ExecCreateOptions{
		AttachStdout: true,
		AttachStderr: true,
		Cmd:          cfg.Cmd,
		Env:          cfg.Env,
		WorkingDir:   cfg.WorkingDir,
	}
	res, err := c.cli.ExecCreate(ctx, containerID, opts)
	if err != nil {
		return "", c.mapErr(http.MethodPost, "/containers/"+containerID+"/exec", err)
	}
	return res.ID, nil
}

// negotiatedVersion returns the Engine API version this client has
// negotiated with the daemon, forcing negotiation first when cfg needs it
// (Env or WorkingDir, each gated by a minimum API version) and it has not
// happened yet. Every other request already triggers negotiation lazily, so
// in the common case ExecCreate is not the first call and this costs
// nothing beyond reading an atomic flag.
func (c *Client) negotiatedVersion(ctx context.Context, cfg dockerclient.ExecConfig) string {
	needsVersion := len(cfg.Env) > 0 || cfg.WorkingDir != ""
	if v := c.cli.ClientVersion(); v != "" || !needsVersion {
		return v
	}
	_, _ = c.cli.Ping(ctx, client.PingOptions{NegotiateAPIVersion: true})
	return c.cli.ClientVersion()
}

// ExecStartTo runs a created exec instance, writing its stdout and stderr
// into the given writers as the process produces them. Either writer may be
// nil to discard that stream.
func (c *Client) ExecStartTo(ctx context.Context, execID string, stdout, stderr io.Writer) error {
	execID, err := dockerclient.CheckID("exec", execID)
	if err != nil {
		return err
	}
	if stdout == nil {
		stdout = io.Discard
	}
	if stderr == nil {
		stderr = io.Discard
	}
	attached, err := c.cli.ExecAttach(ctx, execID, client.ExecAttachOptions{})
	if err != nil {
		return c.mapErr(http.MethodPost, "/exec/"+execID+"/start", err)
	}
	defer attached.Close()
	if _, err := stdcopy.StdCopy(stdout, stderr, attached.Reader); err != nil {
		// stdcopy reports a daemon-side error frame as a plain error with a
		// fixed prefix; reword it to match the message dockerapi produces
		// for the same condition, so a caller sees the same text whichever
		// client is in use.
		if msg, ok := strings.CutPrefix(err.Error(), "error from daemon in stream: "); ok {
			return &dockerclient.StreamError{Problem: "the daemon reported an error in the exec stream: " + msg}
		}
		return &dockerclient.StreamError{Problem: "reading an exec stream frame", Err: err}
	}
	return nil
}

// ExecInspect reports whether an exec instance is still running and, once
// it is not, its exit code.
func (c *Client) ExecInspect(ctx context.Context, execID string) (dockerclient.ExecInspect, error) {
	execID, err := dockerclient.CheckID("exec", execID)
	if err != nil {
		return dockerclient.ExecInspect{}, err
	}
	res, err := c.cli.ExecInspect(ctx, execID, client.ExecInspectOptions{})
	if err != nil {
		return dockerclient.ExecInspect{}, c.mapErr(http.MethodGet, "/exec/"+execID+"/json", err)
	}
	return dockerclient.ExecInspect{ID: res.ID, Running: res.Running, ExitCode: res.ExitCode, ContainerID: res.ContainerID}, nil
}

// execExitWait bounds how long Exec waits for the daemon to report the exit
// code after the output stream has closed.
const execExitWait = 5 * time.Second

// Exec runs a command inside a container, waits for it to finish, and
// returns its output and exit code. A non-zero exit code is reported in the
// result, not as an error, because a failing command is a normal outcome
// the caller usually wants to inspect.
func (c *Client) Exec(ctx context.Context, containerID string, cmd ...string) (dockerclient.ExecResult, error) {
	if len(cmd) == 0 {
		return dockerclient.ExecResult{}, dockerclient.ErrNoCommand
	}
	execID, err := c.ExecCreate(ctx, containerID, dockerclient.ExecConfig{Cmd: cmd})
	if err != nil {
		return dockerclient.ExecResult{}, err
	}
	var out, errOut strings.Builder
	if err := c.ExecStartTo(ctx, execID, &out, &errOut); err != nil {
		return dockerclient.ExecResult{}, err
	}
	res := dockerclient.ExecResult{Stdout: out.String(), Stderr: errOut.String()}

	// The daemon can report the process as running for a moment after the
	// stream closes; poll briefly for the exit code.
	deadline := time.Now().Add(execExitWait)
	for {
		ins, err := c.ExecInspect(ctx, execID)
		if err != nil {
			return res, err
		}
		if !ins.Running {
			res.ExitCode = ins.ExitCode
			return res, nil
		}
		if time.Now().After(deadline) {
			return res, &dockerclient.StreamError{
				Problem: "exec " + execID + " was still running " + execExitWait.String() + " after its output closed; the process may be detached from its stdio, check it with `docker exec` by hand",
				Err:     dockerclient.ErrExecUnfinished,
			}
		}
		select {
		case <-ctx.Done():
			return res, ctx.Err()
		case <-time.After(50 * time.Millisecond):
		}
	}
}
