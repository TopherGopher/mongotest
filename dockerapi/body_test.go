package dockerapi

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// captureBody answers every request and records what arrived.
func captureBody(t *testing.T, got *[]byte, ct *string) func(*http.Request) (*http.Response, error) {
	t.Helper()
	return func(r *http.Request) (*http.Response, error) {
		if r.Body != nil {
			b, err := io.ReadAll(r.Body)
			require.NoError(t, err, "reading the request body the client sent")
			*got = b
		}
		*ct = r.Header.Get("Content-Type")
		return mockStatus(http.StatusOK)(r)
	}
}

func TestDoPassesAReaderBodyThrough(t *testing.T) {
	// A caller with a body already in hand should not have it re-encoded.
	// Marshalling an io.Reader as JSON produces whatever its struct fields
	// happen to serialise to, which is silently wrong rather than an error.
	var got []byte
	var ct string
	c := newMockClient(t, captureBody(t, &got, &ct))

	const raw = `{"Image":"mongo:8"}`
	resp, err := c.do(context.Background(), http.MethodPost, "/containers/create", nil, strings.NewReader(raw))
	require.NoError(t, err, "a reader body must be accepted")
	require.NoError(t, resp.Body.Close(), "closing the response")

	assert.Equal(t, raw, string(got), "the reader's bytes must reach the daemon unchanged")
	assert.Equal(t, "application/json", ct, "a reader body through do is still JSON")
}

func TestDoStillEncodesAValueBody(t *testing.T) {
	// The common path must be untouched: a struct is marshalled as before.
	var got []byte
	var ct string
	c := newMockClient(t, captureBody(t, &got, &ct))

	resp, err := c.do(context.Background(), http.MethodPost, "/containers/create", nil,
		ContainerConfig{Image: "mongo:8"})
	require.NoError(t, err, "a value body must still be accepted")
	require.NoError(t, resp.Body.Close(), "closing the response")

	assert.Contains(t, string(got), `"Image":"mongo:8"`, "a value body must be JSON encoded")
	assert.Equal(t, "application/json", ct, "a value body is JSON")
}

func TestDoSendsNoBodyForNil(t *testing.T) {
	var got []byte
	var ct string
	c := newMockClient(t, captureBody(t, &got, &ct))

	resp, err := c.do(context.Background(), http.MethodGet, "/containers/x/json", nil, nil)
	require.NoError(t, err, "a nil body must be accepted")
	require.NoError(t, resp.Body.Close(), "closing the response")

	assert.Empty(t, got, "a nil body must send nothing")
	assert.Empty(t, ct, "a request with no body must not claim a content type")
}

func TestDoTreatsATypedNilReaderAsNoBody(t *testing.T) {
	// A nil *strings.Reader stored in an any is not == nil, so a naive
	// check would pass it to http.NewRequest and panic on the first read.
	var got []byte
	var ct string
	c := newMockClient(t, captureBody(t, &got, &ct))

	var nilReader *strings.Reader
	resp, err := c.do(context.Background(), http.MethodGet, "/containers/x/json", nil, nilReader)
	require.NoError(t, err, "a typed nil reader must not blow up")
	require.NoError(t, resp.Body.Close(), "closing the response")

	assert.Empty(t, got, "a typed nil reader must send no body")
}
