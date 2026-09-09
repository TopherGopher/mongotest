package dockerapi

import (
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
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
	assert.Equal(t, "the daemon gave no message", mk(368, "").Message, "an unregistered status code with no body still gets a message")
	assert.Len(t, mk(500, strings.Repeat("x", 500)).Message, 200, "long bodies are trimmed to the byte cap so a stray HTML page cannot fill a log line")

	wrapped := &wrapErr{err: mk(404, "")}
	assert.True(t, IsNotFound(wrapped), "wrapped status errors must still match through errors.Is")
}

type wrapErr struct{ err error }

func (w *wrapErr) Error() string { return "wrapped: " + w.err.Error() }
func (w *wrapErr) Unwrap() error { return w.err }
