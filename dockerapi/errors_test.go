package dockerapi

import (
	"errors"
	"io"
	"io/fs"
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestStatusErrorMapping(t *testing.T) {
	mk := func(code int, body string) *StatusError {
		resp := &http.Response{StatusCode: code, Body: io.NopCloser(strings.NewReader(body))}
		return newStatusError(resp, "GET", "/containers/x/json")
	}
	e := mk(404, `{"message":"No such container: x"}`)
	assert.ErrorIs(t, e, ErrNotFound, "404 must match ErrNotFound")
	assert.True(t, IsNotFound(e), "IsNotFound is the convenience for 404")
	assert.NotErrorIs(t, e, ErrConflict, "404 must not match ErrConflict")
	assert.Contains(t, e.Error(), "rejected GET /containers/x/json with status 404: No such container: x", "the message must tell what was asked and what the daemon said")
	assert.Contains(t, e.Error(), "Check the id, name or image tag", "a 404 must carry a hint about what to check")

	c := mk(409, `{"message":"in use"}`)
	assert.ErrorIs(t, c, ErrConflict, "409 must match ErrConflict")
	assert.NotErrorIs(t, c, ErrNotFound, "409 must not match ErrNotFound")
	assert.Contains(t, c.Error(), "retry shortly", "a 409 must carry a retry hint")

	u := mk(401, `{"message":"unauthorized"}`)
	assert.ErrorIs(t, u, ErrUnauthorized, "401 must match ErrUnauthorized")
	assert.ErrorIs(t, mk(403, ""), ErrUnauthorized, "403 must match ErrUnauthorized")
	assert.Contains(t, u.Error(), "docker login", "an auth failure must point at docker login")

	raw := mk(500, "<html>oops</html>")
	assert.Equal(t, "<html>oops</html>", raw.Message, "a non-JSON body is kept verbatim as the message")
	assert.Contains(t, raw.Error(), "500", "the status code must appear in the message")
	assert.Contains(t, raw.Error(), "journalctl -u docker", "a 500 must tell the user where to look")

	assert.Equal(t, "Bad Gateway", mk(502, "").Message, "an empty body falls back to the status text")
	assert.Len(t, mk(500, strings.Repeat("x", 500)).Message, 200, "long bodies are trimmed to 200 characters")

	wrapped := &wrapErr{err: mk(404, "")}
	assert.True(t, IsNotFound(wrapped), "wrapped status errors must still match through errors.Is")
}

type wrapErr struct{ err error }

func (w *wrapErr) Error() string { return "wrapped: " + w.err.Error() }
func (w *wrapErr) Unwrap() error { return w.err }

func TestInvalidArgumentErrorMessage(t *testing.T) {
	err := invalidArg("container id", "a/b", "only letters are allowed", "pass the id from ContainerCreate")
	assert.ErrorIs(t, err, ErrInvalidArgument, "every InvalidArgumentError matches the sentinel")
	assert.Equal(t, `dockerapi: invalid container id "a/b": only letters are allowed. pass the id from ContainerCreate`, err.Error(), "the message follows argument, value, problem, fix")
	var ia *InvalidArgumentError
	require.True(t, errors.As(err, &ia), "callers can extract the typed error")
	assert.Equal(t, "a/b", ia.Value, "the offending value is available to callers")
	assert.Same(t, ErrNoCommand, ErrNoCommand, "predeclared argument errors are comparable values")
	assert.ErrorIs(t, ErrNoCommand, ErrInvalidArgument, "predeclared argument errors also match the sentinel")
}

func TestConnectionErrorWrapping(t *testing.T) {
	err := wrapConnError("unix:///var/run/docker.sock", fs.ErrPermission)
	require.ErrorIs(t, err, ErrConnectionFailed, "permission denied on the socket is a connection failure")
	assert.Contains(t, err.Error(), "permission denied", "the problem must be stated")
	assert.Contains(t, err.Error(), "docker group", "the fix must mention the docker group")
	var ce *ConnectionError
	require.True(t, errors.As(err, &ce), "callers can extract the typed error")
	assert.Equal(t, "unix:///var/run/docker.sock", ce.Host, "the host is carried on the error")

	err = wrapConnError("unix:///x.sock", fs.ErrNotExist)
	assert.ErrorIs(t, err, ErrConnectionFailed, "a missing socket is a connection failure")
	assert.Contains(t, err.Error(), "does not exist", "the problem must be stated")
	assert.Contains(t, err.Error(), "Start the docker daemon", "the fix must say to start the daemon")

	assert.Nil(t, wrapConnError("h", nil), "nil stays nil")
	plain := errors.New("something else")
	assert.Same(t, plain, wrapConnError("h", plain), "errors that are not connection problems pass through unchanged")
}

func TestAPIVersionErrorMessages(t *testing.T) {
	feature := &APIVersionError{Feature: "ExecConfig.WorkingDir", Required: "1.35", Negotiated: "1.30"}
	assert.ErrorIs(t, feature, ErrAPIVersion, "feature gates match the sentinel")
	assert.Contains(t, feature.Error(), "1.35", "the required version is named")
	assert.Contains(t, feature.Error(), "1.30", "the negotiated version is named")
	assert.Contains(t, feature.Error(), "drop the ExecConfig.WorkingDir option", "the fix is stated")
}

func TestResponseErrorMessage(t *testing.T) {
	err := decodeError("GET", "/containers/x/json", io.ErrUnexpectedEOF)
	assert.ErrorIs(t, err, ErrDaemonResponse, "decode failures match the sentinel")
	assert.ErrorIs(t, err, io.ErrUnexpectedEOF, "the underlying decode error is wrapped")
	assert.Contains(t, err.Error(), "DOCKER_API_VERSION", "the message points at the version pin as the likely cause")
	assert.Contains(t, unexpectedStatus("POST", "/x", 202).Error(), "unexpected status 202", "unexpected statuses are named")
}
