package dockerapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net"
	"net/http"
	"strings"
)

// Sentinel errors that StatusError values match through errors.Is.
var (
	// ErrNotFound is matched by any 404 response (unknown container, image,
	// exec instance or path).
	ErrNotFound = errors.New("not found")
	// ErrConflict is matched by any 409 response (for example a container
	// name already in use).
	ErrConflict = errors.New("conflict")
	// ErrUnauthorized is matched by 401 and 403 responses (for example a
	// pull from a registry that needs credentials).
	ErrUnauthorized = errors.New("unauthorized")
	// ErrConnectionFailed is matched when the daemon could not be reached at
	// all: socket missing, permission denied, connection refused, timeout.
	ErrConnectionFailed = errors.New("cannot connect to the docker daemon")
)

// StatusError is returned for any non-2xx daemon response.
type StatusError struct {
	StatusCode int
	// Message is the daemon's {"message": ...} body, or the raw body when it
	// was not JSON.
	Message string
	Method  string
	Path    string
}

func (e *StatusError) Error() string {
	return fmt.Sprintf("docker: %s %s: %d %s", e.Method, e.Path, e.StatusCode, e.Message)
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

// newStatusError reads the response body and builds a StatusError. The body
// is consumed; the caller still closes it.
func newStatusError(resp *http.Response, method, path string) *StatusError {
	const maxBody = 1 << 20
	b, _ := io.ReadAll(io.LimitReader(resp.Body, maxBody))
	msg := strings.TrimSpace(string(b))
	var body struct {
		Message string `json:"message"`
	}
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

// connError is a transport-level failure to reach the daemon.
type connError struct {
	msg string
	err error
}

func (e *connError) Error() string { return e.msg }
func (e *connError) Unwrap() error { return e.err }
func (e *connError) Is(target error) bool {
	return target == ErrConnectionFailed
}

// wrapConnError turns a dial or round-trip failure into an actionable
// message, following the docker CLI's wording. Context errors are returned
// untouched so callers can compare them directly.
func wrapConnError(host string, err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	switch {
	case errors.Is(err, fs.ErrPermission):
		return &connError{err: err, msg: fmt.Sprintf("dockerapi: permission denied while trying to connect to the docker API at %s (is your user in the docker group, or is DOCKER_HOST pointing at the right socket?)", host)}
	case errors.Is(err, fs.ErrNotExist):
		return &connError{err: err, msg: fmt.Sprintf("dockerapi: cannot connect to the docker API at %s: the socket does not exist; is the docker daemon running, and does DOCKER_HOST point at it?", host)}
	}
	msg := err.Error()
	if strings.Contains(msg, "bad certificate") || strings.Contains(msg, "handshake failure") {
		return &connError{err: err, msg: fmt.Sprintf("dockerapi: TLS handshake with %s failed: the daemon probably requires a client certificate (DOCKER_CERT_PATH with cert.pem and key.pem): %v", host, err)}
	}
	var dnsErr *net.DNSError
	if errors.As(err, &dnsErr) {
		return &connError{err: err, msg: fmt.Sprintf("dockerapi: cannot resolve the docker host %s: %v", host, dnsErr)}
	}
	var nErr net.Error
	if errors.As(err, &nErr) || strings.Contains(msg, "connection refused") {
		return &connError{err: err, msg: fmt.Sprintf("dockerapi: cannot connect to the docker daemon at %s. Is the docker daemon running? (%v)", host, rootCause(err))}
	}
	return err
}

// rootCause strips the url.Error wrapper so messages do not repeat the
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
