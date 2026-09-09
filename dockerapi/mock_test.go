package dockerapi

import (
	"bytes"
	"encoding/json/v2"
	"fmt"
	"io"
	"net/http"
	"testing"

	"github.com/stretchr/testify/require"
)

// In-process stubbing, following the pattern the official moby client uses
// for its own unit tests: an http.RoundTripper function stands in for the
// daemon, so request shape and error handling can be asserted without a
// socket. Negotiation is answered automatically. Hijacked exec streams need
// a real connection and are covered by the fakedaemon-based tests instead.

type mockRoundTripper func(*http.Request) (*http.Response, error)

func (f mockRoundTripper) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

// newMockClient returns a Client whose requests are answered by fn. The
// daemon's version window is served automatically as 1.54/1.40 unless fn
// handles /version itself.
func newMockClient(t testing.TB, fn func(*http.Request) (*http.Response, error), opts ...Option) *Client {
	t.Helper()
	rt := mockRoundTripper(func(req *http.Request) (*http.Response, error) {
		if req.URL.Path == "/version" {
			return mockJSON(http.StatusOK, versionResponse{APIVersion: "1.54", MinAPIVersion: "1.40"})(req)
		}
		resp, err := fn(req)
		if resp != nil {
			if resp.Body == nil {
				resp.Body = http.NoBody
			}
			if resp.Request == nil {
				resp.Request = req
			}
		}
		return resp, err
	})
	all := append([]Option{WithHost("unix:///mock/docker.sock"), WithHTTPClient(&http.Client{Transport: rt})}, opts...)
	c, err := New(all...)
	require.NoError(t, err, "a mock client must always construct; WithHost and WithHTTPClient are both valid")
	return c
}

// mockJSON answers with a JSON body.
func mockJSON(status int, v any) func(*http.Request) (*http.Response, error) {
	return func(req *http.Request) (*http.Response, error) {
		b, err := json.Marshal(v)
		if err != nil {
			return nil, err
		}
		return &http.Response{
			StatusCode: status,
			Header:     http.Header{"Content-Type": {"application/json"}},
			Body:       io.NopCloser(bytes.NewReader(b)),
			Request:    req,
		}, nil
	}
}

// mockStatus answers with a status and no body.
func mockStatus(status int) func(*http.Request) (*http.Response, error) {
	return func(req *http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: status, Body: http.NoBody, Request: req}, nil
	}
}

// errorMock answers every request with the daemon's error envelope.
func errorMock(status int, message string) func(*http.Request) (*http.Response, error) {
	return mockJSON(status, errorResponse{Message: message})
}

// assertRequest checks method and versioned path; expectedPath is given
// without the version prefix.
func assertRequest(req *http.Request, method, expectedPath string) error {
	want := "/v" + PreferredAPIVersion + expectedPath
	if req.URL.Path != want {
		return fmt.Errorf("expected URL %q, got %q", want, req.URL.Path)
	}
	if req.Method != method {
		return fmt.Errorf("expected %s, got %s", method, req.Method)
	}
	return nil
}

// assertQuery checks the encoded query string.
func assertQuery(req *http.Request, expected string) error {
	if got := req.URL.Query().Encode(); got != expected {
		return fmt.Errorf("expected query %q, got %q", expected, got)
	}
	return nil
}

// noRequest fails the test if the daemon is reached at all; used to prove
// that client-side validation short-circuits.
func noRequest(t testing.TB) func(*http.Request) (*http.Response, error) {
	return func(req *http.Request) (*http.Response, error) {
		t.Errorf("the daemon must not be reached, but got %s %s", req.Method, req.URL.Path)
		return nil, fmt.Errorf("unexpected request")
	}
}

// decodeBody decodes a JSON request body into v.
func decodeBody(req *http.Request, v any) error {
	defer req.Body.Close()
	return json.UnmarshalRead(req.Body, v)
}
