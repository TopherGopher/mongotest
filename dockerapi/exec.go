package dockerapi

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// ExecConfig describes a command to run inside a container. stdout and
// stderr are always attached and captured in multiplexed (non-TTY) mode so
// the two streams stay separable; a TTY is deliberately not offered.
type ExecConfig struct {
	Cmd        []string
	Env        []string
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

// ExecCreate registers a command to run in a container and returns the exec
// instance id.
func (c *Client) ExecCreate(ctx context.Context, containerID string, cfg ExecConfig) (string, error) {
	path := "/containers/" + containerID + "/exec"
	body := struct {
		AttachStdout bool     `json:"AttachStdout"`
		AttachStderr bool     `json:"AttachStderr"`
		Tty          bool     `json:"Tty"`
		Cmd          []string `json:"Cmd"`
		Env          []string `json:"Env,omitempty"`
		WorkingDir   string   `json:"WorkingDir,omitempty"`
	}{
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
	if resp.StatusCode != http.StatusCreated {
		return "", newStatusError(resp, http.MethodPost, path)
	}
	var out struct {
		ID string `json:"Id"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return "", fmt.Errorf("dockerapi: decode exec create response: %w", err)
	}
	return out.ID, nil
}

// ExecStart runs a created exec instance and returns everything it wrote to
// stdout and stderr. The daemon upgrades the connection to a raw stream, so
// this bypasses http.Client and speaks HTTP/1.1 over a fresh connection.
func (c *Client) ExecStart(ctx context.Context, execID string) (stdout, stderr []byte, err error) {
	if err := c.Negotiate(ctx); err != nil {
		return nil, nil, err
	}
	path := "/v" + c.APIVersion() + "/exec/" + execID + "/start"

	conn, err := c.dial(ctx)
	if err != nil {
		return nil, nil, fmt.Errorf("dockerapi: dial %s for exec start: %w", c.host, err)
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

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+path,
		bytes.NewReader([]byte(`{"Detach":false,"Tty":false}`)))
	if err != nil {
		return nil, nil, fmt.Errorf("dockerapi: build exec start request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Connection", "Upgrade")
	req.Header.Set("Upgrade", "tcp")
	req.Header.Set("User-Agent", c.userAgent)
	if err := req.Write(conn); err != nil {
		return nil, nil, ctxErr(ctx, fmt.Errorf("dockerapi: write exec start request: %w", err))
	}

	br := bufio.NewReader(conn)
	resp, err := http.ReadResponse(br, req)
	if err != nil {
		return nil, nil, ctxErr(ctx, fmt.Errorf("dockerapi: read exec start response: %w", err))
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
		return nil, nil, newStatusError(resp, http.MethodPost, strings.TrimPrefix(path, "/v"+c.APIVersion()))
	}
	stdout, stderr, err = demux(stream)
	if err != nil {
		return stdout, stderr, ctxErr(ctx, err)
	}
	return stdout, stderr, nil
}

// ctxErr prefers the context's error once the context is done, since a
// closed connection surfaces as an unhelpful "use of closed network
// connection" otherwise.
func ctxErr(ctx context.Context, err error) error {
	if ctx.Err() != nil {
		return fmt.Errorf("dockerapi: exec start: %w", ctx.Err())
	}
	return err
}

// ExecInspect returns whether an exec instance is still running and its
// exit code.
func (c *Client) ExecInspect(ctx context.Context, execID string) (ExecInspect, error) {
	var out ExecInspect
	err := c.getJSON(ctx, "/exec/"+execID+"/json", &out)
	return out, err
}

// Exec runs cmd inside a container, waits for it to finish and returns its
// output and exit code. A non-zero exit code is reported in the result, not
// as an error.
func (c *Client) Exec(ctx context.Context, containerID string, cmd ...string) (ExecResult, error) {
	if len(cmd) == 0 {
		return ExecResult{}, errors.New("dockerapi: Exec: empty command")
	}
	execID, err := c.ExecCreate(ctx, containerID, ExecConfig{Cmd: cmd})
	if err != nil {
		return ExecResult{}, err
	}
	stdout, stderr, err := c.ExecStart(ctx, execID)
	if err != nil {
		return ExecResult{}, err
	}
	res := ExecResult{Stdout: string(stdout), Stderr: string(stderr)}

	// The daemon can report the process as running for a moment after the
	// stream closes; poll briefly for the exit code.
	deadline := time.Now().Add(5 * time.Second)
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
			return res, fmt.Errorf("dockerapi: exec %s still running 5s after its output stream closed", execID)
		}
		select {
		case <-ctx.Done():
			return res, ctx.Err()
		case <-time.After(50 * time.Millisecond):
		}
	}
}
