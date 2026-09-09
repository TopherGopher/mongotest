package dockerclient

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"net"
	"net/http"
	"os"
	"strings"
	"syscall"
)

// Every error any implementation returns is one of these sentinels or wraps
// one, so callers branch with errors.Is and never parse a message. The
// concrete types below carry the details and a suggested fix.
var (
	// ErrNotFound is matched by any 404 from the daemon: an unknown
	// container, image, exec instance or path.
	ErrNotFound = errors.New("not found")
	// ErrConflict is matched by any 409: a container name already in use, or
	// an operation already in progress on the same object.
	ErrConflict = errors.New("conflict")
	// ErrUnauthorized is matched by 401 and 403, for example a pull from a
	// registry that needs credentials.
	ErrUnauthorized = errors.New("unauthorized")
	// ErrConnectionFailed is matched when the daemon could not be reached at
	// all: socket missing, permission denied, connection refused, timeout.
	ErrConnectionFailed = errors.New("cannot connect to the docker daemon")
	// ErrInvalidArgument is matched by every client-side validation failure:
	// a bad host string, image reference, id, port or option.
	ErrInvalidArgument = errors.New("invalid argument")
	// ErrAPIVersion is matched when the daemon's API window and the client's
	// do not overlap, or a feature needs a newer API than was negotiated.
	ErrAPIVersion = errors.New("unsupported docker API version")
	// ErrDaemonResponse is matched when the daemon answered with something
	// the client could not interpret: an unexpected status, or a body that
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

// Predeclared errors for the cases whose message never varies, so callers
// can compare them directly as well as with errors.Is.
var (
	// ErrNoCommand is returned by Exec and ExecCreate when no program is
	// given.
	ErrNoCommand = InvalidArgument("command", "", "no program was given",
		`pass the program and its arguments, for example Exec(ctx, containerID, "mongosh", "--quiet", "--eval", "1")`)
	// ErrNoFiles is returned by CopyToContainer when the file list is empty.
	ErrNoFiles = InvalidArgument("files", "", "no files were given",
		"pass at least one File with a relative Name and its Content")
	// ErrEmptyHost is returned when the docker host string is empty.
	ErrEmptyHost = InvalidArgument("docker host", "", "the value is empty",
		"set DOCKER_HOST to unix:///var/run/docker.sock or tcp://host:2375, or pass the client's WithHost option")
)

// InvalidArgumentError describes a value the client refused to send to the
// daemon. It matches ErrInvalidArgument.
type InvalidArgumentError struct {
	// Argument names what was wrong: "image reference", "container id",
	// "PortBindings[27017/tcp]".
	Argument string
	// Value is the offending value, when there is one worth showing.
	Value string
	// Problem says what is wrong with it.
	Problem string
	// Fix says what the caller should do instead.
	Fix string
}

// InvalidArgument builds an InvalidArgumentError. Implementations use it so
// that a rejected value reads the same whichever client produced it.
func InvalidArgument(argument, value, problem, fix string) *InvalidArgumentError {
	return &InvalidArgumentError{Argument: argument, Value: value, Problem: problem, Fix: fix}
}

func (e *InvalidArgumentError) Error() string {
	var b strings.Builder
	b.WriteString("docker: invalid ")
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

// StatusError is returned for any non-2xx daemon response. It matches
// ErrNotFound, ErrConflict or ErrUnauthorized according to the status code.
type StatusError struct {
	StatusCode int
	// Message is the daemon's own message for the failure.
	Message string
	Method  string
	Path    string
}

func (e *StatusError) Error() string {
	msg := fmt.Sprintf("docker: the docker daemon rejected %s %s with status %d: %s", e.Method, e.Path, e.StatusCode, e.Message)
	if hint := e.Hint(); hint != "" {
		msg += ". " + hint
	}
	return msg
}

// Hint suggests what to do about the status code. Error includes it.
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

// Is lets errors.Is match the sentinel for this status code.
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

// IsNotFound reports whether err is a 404 from the daemon. It is the check
// callers make most often, usually to pull an image and retry.
func IsNotFound(err error) bool { return errors.Is(err, ErrNotFound) }

// ResponseError is returned when a reply could not be used: an unexpected
// status, or a body that does not decode. It matches ErrDaemonResponse.
type ResponseError struct {
	Method string
	Path   string
	// Problem says what was wrong with the response.
	Problem string
	Err     error
}

// DecodeError reports that a daemon response could not be decoded.
func DecodeError(method, path string, err error) *ResponseError {
	return &ResponseError{Method: method, Path: path, Problem: "could not decode the daemon's response", Err: err}
}

// UnexpectedStatus reports a success status the endpoint does not document.
func UnexpectedStatus(method, path string, code int) *ResponseError {
	return &ResponseError{Method: method, Path: path, Problem: fmt.Sprintf("unexpected status %d", code)}
}

func (e *ResponseError) Error() string {
	msg := fmt.Sprintf("docker: %s %s: %s", e.Method, e.Path, e.Problem)
	if e.Err != nil {
		msg += ": " + e.Err.Error()
	}
	return msg + ". This usually means the daemon speaks a different API version than negotiated; check `docker version` and any DOCKER_API_VERSION pin"
}

func (e *ResponseError) Unwrap() error        { return e.Err }
func (e *ResponseError) Is(target error) bool { return target == ErrDaemonResponse }

// ConnectionError describes a failure to reach the daemon at all. It matches
// ErrConnectionFailed.
type ConnectionError struct {
	Host string
	// Problem says what went wrong at the transport level.
	Problem string
	// Fix says what the caller should check.
	Fix string
	Err error
}

// ConnectionFailed builds a ConnectionError.
func ConnectionFailed(host, problem, fix string, cause error) *ConnectionError {
	return &ConnectionError{Host: host, Problem: problem, Fix: fix, Err: cause}
}

func (e *ConnectionError) Error() string {
	msg := fmt.Sprintf("docker: cannot connect to the docker daemon at %s: %s", e.Host, e.Problem)
	if e.Fix != "" {
		msg += ". " + e.Fix
	}
	return msg
}

func (e *ConnectionError) Unwrap() error        { return e.Err }
func (e *ConnectionError) Is(target error) bool { return target == ErrConnectionFailed }

// WrapConnectionError turns a dial or round-trip failure into a
// ConnectionError whose message says what to do about it, following the
// docker CLI's wording. Context errors are returned untouched so callers can
// compare them directly, and an error that is not a connection problem is
// returned unchanged. Every implementation routes transport failures through
// this, so the guidance is the same whichever client is in use.
func WrapConnectionError(host string, err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	switch {
	case errors.Is(err, fs.ErrPermission):
		return ConnectionFailed(host, "permission denied",
			"Add your user to the docker group (sudo usermod -aG docker $USER, then log in again), or point DOCKER_HOST at a socket you can access", err)
	case errors.Is(err, fs.ErrNotExist):
		return ConnectionFailed(host, "the socket does not exist",
			"Start the docker daemon (or Docker Desktop, Colima, Podman with its docker socket), or set DOCKER_HOST to where it listens", err)
	}
	msg := err.Error()
	if strings.Contains(msg, "bad certificate") || strings.Contains(msg, "handshake failure") {
		return ConnectionFailed(host, "the TLS handshake failed",
			"The daemon probably requires a client certificate: set DOCKER_CERT_PATH to a directory with ca.pem, cert.pem and key.pem", err)
	}
	var dnsErr *net.DNSError
	if errors.As(err, &dnsErr) {
		return ConnectionFailed(host, "the host name does not resolve ("+dnsErr.Error()+")",
			"Check the host name in DOCKER_HOST", err)
	}
	// Match the concrete conditions, never the net.Error interface.
	// http.Client.Do wraps everything it returns in *url.Error, and
	// *url.Error satisfies net.Error, so an interface check classifies every
	// transport failure as an unreachable daemon. A tar writer failing
	// halfway through an upload would be reported as "start the docker
	// daemon", which is both wrong and unhelpful.
	if isConnectionProblem(err) || strings.Contains(msg, "connection refused") {
		return ConnectionFailed(host, RootCause(err).Error(),
			"Is the docker daemon running? Start it, or set DOCKER_HOST to a daemon that is", err)
	}
	return err
}

// isConnectionProblem reports whether err is a genuine failure to reach the
// daemon, rather than any error that happened to travel through the
// transport.
func isConnectionProblem(err error) bool {
	var opErr *net.OpError
	if errors.As(err, &opErr) {
		return true
	}
	for _, target := range []error{
		syscall.ECONNREFUSED, syscall.ECONNRESET, syscall.ECONNABORTED,
		syscall.EHOSTUNREACH, syscall.ENETUNREACH, syscall.ENETDOWN,
		syscall.EPIPE, syscall.ETIMEDOUT,
		os.ErrDeadlineExceeded,
	} {
		if errors.Is(err, target) {
			return true
		}
	}
	// A timeout is a connection problem whatever produced it, but ask the
	// error rather than assuming: *url.Error delegates Timeout to whatever
	// it wraps, so a non-network cause answers false here.
	var nErr net.Error
	return errors.As(err, &nErr) && nErr.Timeout()
}

// RootCause unwraps err all the way down. Transport errors arrive wrapped in
// url.Error, whose message repeats a placeholder request URL that means
// nothing to the reader.
func RootCause(err error) error {
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
	// Product is the daemon's own description of itself, for example
	// "Docker Engine - Community" or "Podman Engine 5.7.1". When set, the
	// message names it instead of "the docker daemon", because telling
	// someone running Podman to upgrade docker sends them nowhere.
	Product string
	// ServerMin and ServerMax are the daemon's window, when known.
	ServerMin, ServerMax string
	// ClientMax is the newest version the client can speak.
	ClientMax string
	// ClientMin is the oldest version the client can speak.
	ClientMin string
	// Feature and Required are set when one feature, rather than the client
	// as a whole, needs a newer API than Negotiated.
	Feature, Required, Negotiated string
}

func (e *APIVersionError) Error() string {
	// The subject names the daemon as specifically as we can. The fix
	// clause then refers back to it, rather than repeating the name.
	daemon, upgrade := "the docker daemon", "the docker daemon"
	if e.Product != "" {
		daemon, upgrade = e.Product, "the daemon"
	}
	if e.Feature != "" {
		// Here the sentence already ends with what to upgrade, so an
		// unidentified daemon is just "the daemon" rather than repeating.
		subject := "the daemon"
		if e.Product != "" {
			subject = e.Product
		}
		return fmt.Sprintf("docker: %s requires docker API %s or newer but %s negotiated %s. Upgrade %s, or drop the %s option",
			e.Feature, e.Required, subject, e.Negotiated, upgrade, e.Feature)
	}
	if e.ServerMin != "" && e.ClientMax != "" && compareAPIVersions(e.ServerMin, e.ClientMax) > 0 {
		return fmt.Sprintf("docker: %s requires API %s or newer but this client supports at most %s. Upgrade mongotest, or set DOCKER_API_VERSION to a version the daemon accepts if you know it works",
			daemon, e.ServerMin, e.ClientMax)
	}
	return fmt.Sprintf("docker: %s only supports API %s or older but this client needs at least %s. Upgrade %s",
		daemon, e.ServerMax, e.ClientMin, upgrade)
}

func (e *APIVersionError) Is(target error) bool { return target == ErrAPIVersion }

// compareAPIVersions compares two "major.minor" strings numerically, so that
// 1.9 sorts below 1.44. A malformed string sorts lowest.
func compareAPIVersions(a, b string) int {
	am, an := splitAPIVersion(a)
	bm, bn := splitAPIVersion(b)
	switch {
	case am != bm:
		if am < bm {
			return -1
		}
		return 1
	case an != bn:
		if an < bn {
			return -1
		}
		return 1
	}
	return 0
}

func splitAPIVersion(v string) (major, minor int) {
	before, after, ok := strings.Cut(v, ".")
	if !ok {
		return -1, -1
	}
	major, err1 := atoi(before)
	minor, err2 := atoi(after)
	if !err1 || !err2 {
		return -1, -1
	}
	return major, minor
}

// atoi parses a non-negative decimal number, reporting whether it was valid.
func atoi(s string) (int, bool) {
	if s == "" {
		return 0, false
	}
	n := 0
	for _, r := range s {
		if r < '0' || r > '9' {
			return 0, false
		}
		n = n*10 + int(r-'0')
	}
	return n, true
}

// StreamError describes a malformed exec stream, or an error the daemon sent
// inside one. It matches ErrStream.
type StreamError struct {
	Problem string
	Err     error
}

func (e *StreamError) Error() string {
	msg := "docker: " + e.Problem
	if e.Err != nil {
		msg += ": " + e.Err.Error()
	}
	return msg
}

func (e *StreamError) Unwrap() error        { return e.Err }
func (e *StreamError) Is(target error) bool { return target == ErrStream }

// PullError is a failure the daemon reported inside a pull progress stream.
// It matches ErrPull. The HTTP status is 200 in this case, so the error only
// exists because the stream was read to the end.
type PullError struct {
	Ref     string
	Message string
	Err     error
}

func (e *PullError) Error() string {
	msg := fmt.Sprintf("docker: pulling %s failed: %s", e.Ref, e.Message)
	if e.Err != nil {
		msg += ": " + e.Err.Error()
	}
	return msg + ". Check the image name and tag exist on the registry and that this host can reach it"
}

func (e *PullError) Unwrap() error        { return e.Err }
func (e *PullError) Is(target error) bool { return target == ErrPull }
