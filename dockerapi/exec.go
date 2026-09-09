package dockerapi

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json/v2"
	"io"
	"net/http"
	"strings"
	"time"
)

// ExecConfig describes a command to run inside a container. stdout and
// stderr are always attached and captured in multiplexed (non-TTY) mode so
// the two streams stay separable; a TTY is deliberately not offered.
type ExecConfig struct {
	// Cmd is the program and its arguments; required.
	Cmd []string
	// Env holds KEY=value entries (needs API 1.25 or newer).
	Env []string
	// WorkingDir is an absolute path inside the container (needs API 1.35 or
	// newer).
	WorkingDir string
}

// ExecInspect is the state of an exec instance.
type ExecInspect struct {
	ID          string `json:"ID"`
	Running     bool   `json:"Running"`
	ExitCode    int    `json:"ExitCode"`
	ContainerID string `json:"ContainerID"`
}

// ExecResult is what Exec returns. A non-zero ExitCode is not an error.
type ExecResult struct {
	Stdout   string
	Stderr   string
	ExitCode int
}

// execCreateRequest is the body of POST /containers/{id}/exec.
type execCreateRequest struct {
	AttachStdout bool     `json:"AttachStdout"`
	AttachStderr bool     `json:"AttachStderr"`
	Tty          bool     `json:"Tty"`
	Cmd          []string `json:"Cmd"`
	Env          []string `json:"Env,omitempty"`
	WorkingDir   string   `json:"WorkingDir,omitempty"`
}

// execCreateResponse is the body of a successful exec create.
type execCreateResponse struct {
	ID string `json:"Id"`
}

// ExecCreate registers a command to run in a container and returns the exec
// instance id. The config is validated against the negotiated API version.
func (c *Client) ExecCreate(ctx context.Context, containerID string, cfg ExecConfig) (string, error) {
	containerID, err := checkID("container", containerID)
	if err != nil {
		return "", err
	}
	if err := c.Negotiate(ctx); err != nil {
		return "", err
	}
	if err := cfg.validate(c.APIVersion()); err != nil {
		return "", err
	}
	path := "/containers/" + containerID + "/exec"
	body := execCreateRequest{
		AttachStdout: true,
		AttachStderr: true,
		Cmd:          cfg.Cmd,
		Env:          cfg.Env,
		WorkingDir:   cfg.WorkingDir,
	}
	resp, err := c.do(ctx, http.MethodPost, path, nil, body)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	switch {
	case resp.StatusCode == http.StatusCreated:
	case resp.StatusCode >= 400:
		return "", newStatusError(resp, http.MethodPost, path)
	default:
		return "", unexpectedStatus(http.MethodPost, path, resp.StatusCode)
	}
	var out execCreateResponse
	if err := json.UnmarshalRead(resp.Body, &out); err != nil {
		return "", decodeError(http.MethodPost, path, err)
	}
	return out.ID, nil
}

// execStartBody is the fixed body of POST /exec/{id}/start: attached, no TTY.
const execStartBody = `{"Detach":false,"Tty":false}`

// ExecStartTo runs a created exec instance and streams its stdout and stderr
// into the given writers as the process produces them. The daemon upgrades
// the connection to a raw stream, so this bypasses http.Client and speaks
// HTTP/1.1 over a fresh connection. Either writer may be nil to discard that
// stream.
func (c *Client) ExecStartTo(ctx context.Context, execID string, stdout, stderr io.Writer) error {
	execID, err := checkID("exec", execID)
	if err != nil {
		return err
	}
	if err := c.Negotiate(ctx); err != nil {
		return err
	}
	if stdout == nil {
		stdout = io.Discard
	}
	if stderr == nil {
		stderr = io.Discard
	}
	path := "/v" + c.APIVersion() + "/exec/" + execID + "/start"
	unversioned := "/exec/" + execID + "/start"

	conn, err := c.dial(ctx)
	if err != nil {
		return err
	}
	defer conn.Close()

	// Close the connection when ctx ends so a blocked read returns.
	done := make(chan struct{})
	defer close(done)
	go func() {
		select {
		case <-ctx.Done():
			_ = conn.Close()
		case <-done:
		}
	}()

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+path, bytes.NewReader([]byte(execStartBody)))
	if err != nil {
		return &ResponseError{Method: http.MethodPost, Path: unversioned, Problem: "could not build the exec start request", Err: err}
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Connection", "Upgrade")
	req.Header.Set("Upgrade", "tcp")
	req.Header.Set("User-Agent", c.userAgent)
	if err := req.Write(conn); err != nil {
		return ctxErr(ctx, &StreamError{Problem: "writing the exec start request", Err: err})
	}

	br := bufio.NewReader(conn)
	resp, err := http.ReadResponse(br, req)
	if err != nil {
		return ctxErr(ctx, &StreamError{Problem: "reading the exec start response", Err: err})
	}
	var stream io.Reader
	switch resp.StatusCode {
	case http.StatusSwitchingProtocols:
		// The raw stream follows the headers on the same buffered reader.
		stream = br
	case http.StatusOK:
		// Older daemons send the stream as the response body.
		stream = resp.Body
	default:
		defer resp.Body.Close()
		return newStatusError(resp, http.MethodPost, unversioned)
	}
	// The Content-Type header is not a reliable signal for the framing:
	// daemons before API 1.42 always sent application/vnd.docker.raw-stream
	// even for multiplexed output. We never request a TTY, so the stream is
	// always framed and is always demultiplexed.
	if err := demux(stream, stdout, stderr); err != nil {
		return ctxErr(ctx, err)
	}
	return nil
}

// ExecStart runs a created exec instance and returns everything it wrote to
// stdout and stderr. It is ExecStartTo with in-memory buffers; prefer
// ExecStartTo for large or long-running output.
func (c *Client) ExecStart(ctx context.Context, execID string) (stdout, stderr []byte, err error) {
	var out, errOut bytes.Buffer
	err = c.ExecStartTo(ctx, execID, &out, &errOut)
	return out.Bytes(), errOut.Bytes(), err
}

// ctxErr prefers the context's error once the context is done, since a
// closed connection surfaces as an unhelpful "use of closed network
// connection" otherwise.
func ctxErr(ctx context.Context, err error) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	return err
}

// ExecInspect returns whether an exec instance is still running and its
// exit code.
func (c *Client) ExecInspect(ctx context.Context, execID string) (ExecInspect, error) {
	var out ExecInspect
	execID, err := checkID("exec", execID)
	if err != nil {
		return out, err
	}
	err = c.getJSON(ctx, "/exec/"+execID+"/json", &out)
	return out, err
}

// execExitWait bounds how long Exec waits for the daemon to report the exit
// code after the output stream has closed.
const execExitWait = 5 * time.Second

// Exec runs cmd inside a container, waits for it to finish and returns its
// output and exit code. A non-zero exit code is reported in the result, not
// as an error. Output is collected in memory; use ExecCreate, ExecStartTo
// and ExecInspect directly to stream it instead.
func (c *Client) Exec(ctx context.Context, containerID string, cmd ...string) (ExecResult, error) {
	if len(cmd) == 0 {
		return ExecResult{}, ErrNoCommand
	}
	execID, err := c.ExecCreate(ctx, containerID, ExecConfig{Cmd: cmd})
	if err != nil {
		return ExecResult{}, err
	}
	var out, errOut strings.Builder
	if err := c.ExecStartTo(ctx, execID, &out, &errOut); err != nil {
		return ExecResult{}, err
	}
	res := ExecResult{Stdout: out.String(), Stderr: errOut.String()}

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
			return res, &StreamError{Problem: "exec " + execID + " was still running " + execExitWait.String() + " after its output closed; the process may be detached from its stdio, check it with `docker exec` by hand", Err: ErrExecUnfinished}
		}
		select {
		case <-ctx.Done():
			return res, ctx.Err()
		case <-time.After(50 * time.Millisecond):
		}
	}
}
