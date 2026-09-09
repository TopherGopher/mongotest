package dockerapi

import (
	"context"
	"encoding/json/v2"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net"
	"net/http"
	"strings"
)

// Every error returned by this package either is one of these sentinels or
// wraps one, so callers can branch with errors.Is without string matching.
// The concrete types below carry the details and a suggested fix.
var (
	// ErrNotFound is matched by any 404 response (unknown container, image,
	// exec instance or path).
	ErrNotFound = errors.New("not found")
	// ErrConflict is matched by any 409 response (for example a container
	// name already in use, or a removal already in progress).
	ErrConflict = errors.New("conflict")
	// ErrUnauthorized is matched by 401 and 403 responses (for example a
	// pull from a registry that needs credentials).
	ErrUnauthorized = errors.New("unauthorized")
	// ErrConnectionFailed is matched when the daemon could not be reached at
	// all: socket missing, permission denied, connection refused, timeout.
	ErrConnectionFailed = errors.New("cannot connect to the docker daemon")
	// ErrInvalidArgument is matched by every client-side validation failure:
	// a bad host string, image reference, id, port or option.
	ErrInvalidArgument = errors.New("invalid argument")
	// ErrAPIVersion is matched when the daemon's API window and this client
	// do not overlap, or a feature needs a newer API than was negotiated.
	ErrAPIVersion = errors.New("unsupported docker API version")
	// ErrDaemonResponse is matched when the daemon answered with something
	// this client could not interpret: an unexpected status or a body that
	// does not decode.
	ErrDaemonResponse = errors.New("unexpected response from the docker daemon")
	// ErrStream is matched when an exec output stream is malformed or the
	// daemon reported an error inside it.
	ErrStream = errors.New("exec stream failed")
	// ErrPull is matched when the daemon reports a failure inside an image
	// pull progress stream.
	ErrPull = errors.New("image pull failed")
	// ErrExecUnfinished is matched when an exec process is still reported
	// running well after its output stream closed.
	ErrExecUnfinished = errors.New("exec process did not finish")
)

// Predeclared argument errors for the fixed-message cases, so callers can
// compare against them directly.
var (
	// ErrNoCommand is returned by Exec and ExecCreate when no program is
	// given.
	ErrNoCommand = &InvalidArgumentError{
		Argument: "command",
		Problem:  "no program was given",
		Fix:      `pass the program and its arguments, for example Exec(ctx, containerID, "mongosh", "--quiet", "--eval", "1")`,
	}
	// ErrNoFiles is returned by CopyToContainer when the file list is empty.
	ErrNoFiles = &InvalidArgumentError{
		Argument: "files",
		Problem:  "no files were given",
		Fix:      "pass at least one File with a relative Name and its Content",
	}
	// ErrEmptyHost is returned when the docker host string is empty.
	ErrEmptyHost = &InvalidArgumentError{
		Argument: "docker host",
		Problem:  "the value is empty",
		Fix:      "set DOCKER_HOST to unix:///var/run/docker.sock or tcp://host:2375, or pass WithHost",
	}
)

// InvalidArgumentError describes a value this client refused to send to the
// daemon. It matches ErrInvalidArgument.
type InvalidArgumentError struct {
	// Argument names what was wrong: "image reference", "container id",
	// "PortBindings[27017/tcp]".
	Argument string
	// Value is the offending value, when there is one to show.
	Value string
	// Problem says what is wrong with it.
	Problem string
	// Fix says what the caller should do instead.
	Fix string
}

func (e *InvalidArgumentError) Error() string {
	var b strings.Builder
	b.WriteString("dockerapi: invalid ")
	b.WriteString(e.Argument)
	if e.Value != "" {
		fmt.Fprintf(&b, " %q", e.Value)
	}
	if e.Problem != "" {
		b.WriteString(": ")
		b.WriteString(e.Problem)
	}
	if e.Fix != "" {
		b.WriteString(". ")
		b.WriteString(e.Fix)
	}
	return b.String()
}

// Is reports ErrInvalidArgument for every InvalidArgumentError.
func (e *InvalidArgumentError) Is(target error) bool { return target == ErrInvalidArgument }

// invalidArg builds an InvalidArgumentError.
func invalidArg(argument, value, problem, fix string) *InvalidArgumentError {
	return &InvalidArgumentError{Argument: argument, Value: value, Problem: problem, Fix: fix}
}

// StatusError is returned for any non-2xx daemon response. It matches
// ErrNotFound, ErrConflict or ErrUnauthorized according to the status code.
type StatusError struct {
	StatusCode int
	// Message is the daemon's {"message": ...} body, or the raw body when it
	// was not JSON, or the status text when the body was empty.
	Message string
	Method  string
	Path    string
}

func (e *StatusError) Error() string {
	msg := fmt.Sprintf("dockerapi: the docker daemon rejected %s %s with status %d: %s", e.Method, e.Path, e.StatusCode, e.Message)
	if hint := e.Hint(); hint != "" {
		msg += ". " + hint
	}
	return msg
}

// Hint suggests what to do about the status code.
func (e *StatusError) Hint() string {
	switch e.StatusCode {
	case http.StatusBadRequest:
		return "The daemon considered the request malformed; the message names the field to fix"
	case http.StatusUnauthorized, http.StatusForbidden:
		return "Credentials were refused; log in with `docker login`, or use an image from a public registry"
	case http.StatusNotFound:
		return "Check the id, name or image tag; the object may have been removed already, or the image was never pulled"
	case http.StatusConflict:
		return "Another operation on this object is in progress or its name is taken; retry shortly, pick another name, or remove with Force"
	case http.StatusInternalServerError:
		return "The daemon hit an internal error; check its logs (journalctl -u docker, or Docker Desktop's Troubleshoot view) and retry"
	case http.StatusServiceUnavailable:
		return "The daemon is not ready yet; wait a moment and retry"
	}
	return ""
}

// Is lets errors.Is(err, ErrNotFound), ErrConflict and ErrUnauthorized work.
func (e *StatusError) Is(target error) bool {
	switch target {
	case ErrNotFound:
		return e.StatusCode == http.StatusNotFound
	case ErrConflict:
		return e.StatusCode == http.StatusConflict
	case ErrUnauthorized:
		return e.StatusCode == http.StatusUnauthorized || e.StatusCode == http.StatusForbidden
	}
	return false
}

// IsNotFound reports whether err is a 404 from the daemon.
func IsNotFound(err error) bool { return errors.Is(err, ErrNotFound) }

// errorResponse is the daemon's JSON error envelope.
type errorResponse struct {
	Message string `json:"message"`
}

// newStatusError reads the response body and builds a StatusError. The body
// is consumed; the caller still closes it.
func newStatusError(resp *http.Response, method, path string) *StatusError {
	const maxBody = 1 << 20
	b, _ := io.ReadAll(io.LimitReader(resp.Body, maxBody))
	msg := strings.TrimSpace(string(b))
	var body errorResponse
	if json.Unmarshal(b, &body) == nil && body.Message != "" {
		msg = body.Message
	}
	if len(msg) > 200 {
		msg = msg[:200]
	}
	if msg == "" {
		msg = http.StatusText(resp.StatusCode)
	}
	return &StatusError{StatusCode: resp.StatusCode, Message: msg, Method: method, Path: path}
}

// ResponseError is returned when a 2xx answer could not be used: an
// unexpected success status or a body that does not decode. It matches
// ErrDaemonResponse.
type ResponseError struct {
	Method string
	Path   string
	// Problem says what was wrong with the response.
	Problem string
	Err     error
}

func (e *ResponseError) Error() string {
	msg := fmt.Sprintf("dockerapi: %s %s: %s", e.Method, e.Path, e.Problem)
	if e.Err != nil {
		msg += ": " + e.Err.Error()
	}
	return msg + ". This usually means the daemon speaks a different API version than negotiated; check `docker version` and any DOCKER_API_VERSION pin"
}

func (e *ResponseError) Unwrap() error        { return e.Err }
func (e *ResponseError) Is(target error) bool { return target == ErrDaemonResponse }

// decodeError wraps a JSON decode failure of a daemon response.
func decodeError(method, path string, err error) *ResponseError {
	return &ResponseError{Method: method, Path: path, Problem: "could not decode the daemon's response", Err: err}
}

// unexpectedStatus wraps a success status the endpoint does not document.
func unexpectedStatus(method, path string, code int) *ResponseError {
	return &ResponseError{Method: method, Path: path, Problem: fmt.Sprintf("unexpected status %d", code)}
}

// ConnectionError describes a failure to reach the daemon at all. It
// matches ErrConnectionFailed.
type ConnectionError struct {
	Host string
	// Problem says what went wrong at the transport level.
	Problem string
	// Fix says what the caller should check.
	Fix string
	Err error
}

func (e *ConnectionError) Error() string {
	msg := fmt.Sprintf("dockerapi: cannot connect to the docker daemon at %s: %s", e.Host, e.Problem)
	if e.Fix != "" {
		msg += ". " + e.Fix
	}
	return msg
}

func (e *ConnectionError) Unwrap() error        { return e.Err }
func (e *ConnectionError) Is(target error) bool { return target == ErrConnectionFailed }

// wrapConnError turns a dial or round-trip failure into a ConnectionError
// with an actionable fix, following the docker CLI's wording. Context errors
// are returned untouched so callers can compare them directly; errors that
// are not connection problems are returned as they are.
func wrapConnError(host string, err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	switch {
	case errors.Is(err, fs.ErrPermission):
		return &ConnectionError{Host: host, Err: err,
			Problem: "permission denied",
			Fix:     "Add your user to the docker group (sudo usermod -aG docker $USER, then log in again), or point DOCKER_HOST at a socket you can access"}
	case errors.Is(err, fs.ErrNotExist):
		return &ConnectionError{Host: host, Err: err,
			Problem: "the socket does not exist",
			Fix:     "Start the docker daemon (or Docker Desktop, Colima, Podman with its docker socket), or set DOCKER_HOST to where it listens"}
	}
	msg := err.Error()
	if strings.Contains(msg, "bad certificate") || strings.Contains(msg, "handshake failure") {
		return &ConnectionError{Host: host, Err: err,
			Problem: "the TLS handshake failed",
			Fix:     "The daemon probably requires a client certificate: set DOCKER_CERT_PATH to a directory with ca.pem, cert.pem and key.pem"}
	}
	var dnsErr *net.DNSError
	if errors.As(err, &dnsErr) {
		return &ConnectionError{Host: host, Err: err,
			Problem: "the host name does not resolve (" + dnsErr.Error() + ")",
			Fix:     "Check the host name in DOCKER_HOST"}
	}
	var nErr net.Error
	if errors.As(err, &nErr) || strings.Contains(msg, "connection refused") {
		return &ConnectionError{Host: host, Err: err,
			Problem: rootCause(err).Error(),
			Fix:     "Is the docker daemon running? Start it, or set DOCKER_HOST to a daemon that is"}
	}
	return err
}

// rootCause strips wrappers such as url.Error so messages do not repeat the
// placeholder request URL.
func rootCause(err error) error {
	for {
		u := errors.Unwrap(err)
		if u == nil {
			return err
		}
		err = u
	}
}

// APIVersionError describes a version mismatch with the daemon. It matches
// ErrAPIVersion.
type APIVersionError struct {
	// ServerMin and ServerMax are the daemon's window, when known.
	ServerMin, ServerMax string
	// Feature and Required are set when a specific feature needs a newer
	// API than Negotiated.
	Feature, Required, Negotiated string
}

func (e *APIVersionError) Error() string {
	if e.Feature != "" {
		return fmt.Sprintf("dockerapi: %s requires docker API %s or newer but the daemon negotiated %s. Upgrade the docker daemon, or drop the %s option",
			e.Feature, e.Required, e.Negotiated, e.Feature)
	}
	if e.ServerMin != "" && compareVersions(e.ServerMin, PreferredAPIVersion) > 0 {
		return fmt.Sprintf("dockerapi: the docker daemon requires API %s or newer but this client supports at most %s. Upgrade mongotest, or set DOCKER_API_VERSION to a version the daemon accepts if you know it works",
			e.ServerMin, PreferredAPIVersion)
	}
	return fmt.Sprintf("dockerapi: the docker daemon only supports API %s or older but this client needs at least %s. Upgrade the docker daemon",
		e.ServerMax, MinSupportedAPIVersion)
}

func (e *APIVersionError) Is(target error) bool { return target == ErrAPIVersion }

// StreamError describes a malformed exec stream or an error the daemon sent
// inside it. It matches ErrStream.
type StreamError struct {
	Problem string
	Err     error
}

func (e *StreamError) Error() string {
	msg := "dockerapi: " + e.Problem
	if e.Err != nil {
		msg += ": " + e.Err.Error()
	}
	return msg
}

func (e *StreamError) Unwrap() error        { return e.Err }
func (e *StreamError) Is(target error) bool { return target == ErrStream }

// PullError is an error the daemon reported inside a pull progress stream.
// It matches ErrPull.
type PullError struct {
	Ref     string
	Message string
	Err     error
}

func (e *PullError) Error() string {
	msg := fmt.Sprintf("dockerapi: pulling %s failed: %s", e.Ref, e.Message)
	if e.Err != nil {
		msg += ": " + e.Err.Error()
	}
	return msg + ". Check the image name and tag exist on the registry and that this host can reach it"
}

func (e *PullError) Unwrap() error        { return e.Err }
func (e *PullError) Is(target error) bool { return target == ErrPull }
