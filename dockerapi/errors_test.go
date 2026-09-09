package dockerapi

import (
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
)

func TestStatusErrorMapping(t *testing.T) {
	mk := func(code int, body string) *StatusError {
		resp := &http.Response{StatusCode: code, Body: io.NopCloser(strings.NewReader(body))}
		return newStatusError(resp, "GET", "/containers/x/json")
	}
	e := mk(404, `{"message":"No such container: x"}`)
	if !errors.Is(e, ErrNotFound) || !IsNotFound(e) || errors.Is(e, ErrConflict) {
		t.Fatalf("404 mapping wrong: %v", e)
	}
	if e.Error() != "docker: GET /containers/x/json: 404 No such container: x" {
		t.Fatalf("Error() = %q", e.Error())
	}
	if c := mk(409, `{"message":"in use"}`); !errors.Is(c, ErrConflict) || errors.Is(c, ErrNotFound) {
		t.Fatalf("409 mapping wrong: %v", c)
	}
	if raw := mk(500, "<html>oops</html>"); raw.Message != "<html>oops</html>" || !strings.Contains(raw.Error(), "500") {
		t.Fatalf("non-JSON body: %v", raw)
	}
	if empty := mk(502, ""); empty.Message != "Bad Gateway" {
		t.Fatalf("empty body message %q", empty.Message)
	}
	if long := mk(500, strings.Repeat("x", 500)); len(long.Message) != 200 {
		t.Fatalf("message not trimmed: %d", len(long.Message))
	}
	// Wrapped errors still match.
	wrapped := &wrapErr{err: mk(404, "")}
	if !IsNotFound(wrapped) {
		t.Fatal("wrapped 404 should match")
	}
}

type wrapErr struct{ err error }

func (w *wrapErr) Error() string { return "wrapped: " + w.err.Error() }
func (w *wrapErr) Unwrap() error { return w.err }
