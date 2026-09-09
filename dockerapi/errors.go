package dockerapi

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
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

// Is lets errors.Is(err, ErrNotFound) and errors.Is(err, ErrConflict) work.
func (e *StatusError) Is(target error) bool {
	switch target {
	case ErrNotFound:
		return e.StatusCode == http.StatusNotFound
	case ErrConflict:
		return e.StatusCode == http.StatusConflict
	}
	return false
}

// IsNotFound reports whether err is a 404 from the daemon.
func IsNotFound(err error) bool { return errors.Is(err, ErrNotFound) }

// newStatusError reads the response body and builds a StatusError. The body
// is consumed; the caller still closes it.
func newStatusError(resp *http.Response, method, path string) *StatusError {
	const maxBody = 4096
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
