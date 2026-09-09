package fakedaemon

import (
	"context"
	"encoding/json/v2"
	"io"
	"net"
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func unixClient(sock string) *http.Client {
	return &http.Client{Transport: &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			var d net.Dialer
			return d.DialContext(ctx, "unix", sock)
		},
	}}
}

func TestRoutesVersionPrefixAndParams(t *testing.T) {
	s := New(t)
	s.Handle("GET", "/containers/{id}/json", func(w http.ResponseWriter, r *http.Request) {
		JSON(w, 200, map[string]string{"Id": PathParam(r, "id")})
	})
	c := unixClient(strings.TrimPrefix(s.Host(), "unix://"))

	for _, p := range []string{"/containers/abc/json", "/v1.44/containers/abc/json"} {
		req, err := http.NewRequest("GET", "http://api.moby.localhost"+p+"?size=1", strings.NewReader("hello"))
		require.NoError(t, err, "building request for %s", p)
		resp, err := c.Do(req)
		require.NoError(t, err, "request %s over the unix socket", p)
		var got map[string]string
		require.NoError(t, json.UnmarshalRead(resp.Body, &got), "%s: response decodes", p)
		resp.Body.Close()
		assert.Equal(t, 200, resp.StatusCode, "%s: the route must match with and without a version prefix", p)
		assert.Equal(t, "abc", got["Id"], "%s: the {id} segment must be available through PathParam", p)
	}
	reqs := s.Requests()
	require.Len(t, reqs, 2, "both requests must be recorded")
	assert.Equal(t, "/containers/abc/json", reqs[1].Path, "Path has the version prefix removed")
	assert.Equal(t, "/v1.44/containers/abc/json", reqs[1].RawPath, "RawPath keeps the version prefix")
	assert.Equal(t, "hello", string(reqs[1].Body), "the body is recorded")
	assert.Equal(t, "1", reqs[1].Query.Get("size"), "the query is recorded")
	assert.Equal(t, "GET", reqs[1].Method, "the method is recorded")
}

func TestUnmatchedIs404JSON(t *testing.T) {
	s := NewTCP(t)
	resp, err := http.Get(s.URL() + "/v1.41/nope")
	require.NoError(t, err, "request to an unregistered route")
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	assert.Equal(t, 404, resp.StatusCode, "unregistered routes answer 404 like the daemon")
	assert.JSONEq(t, `{"message":"page not found"}`, string(b), "the body is the daemon's 404 envelope")

	s.Handle("POST", "/x", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(204) })
	resp2, err := http.Get(s.URL() + "/x")
	require.NoError(t, err, "GET to a POST-only route")
	resp2.Body.Close()
	assert.Equal(t, 404, resp2.StatusCode, "routes must match on method too")
}

func TestServeVersion(t *testing.T) {
	s := NewTCP(t)
	s.ServeVersion("1.54", "1.40")
	resp, err := http.Get(s.URL() + "/version")
	require.NoError(t, err, "GET /version")
	defer resp.Body.Close()
	var v struct {
		APIVersion    string `json:"ApiVersion"`
		MinAPIVersion string `json:"MinAPIVersion"`
	}
	require.NoError(t, json.UnmarshalRead(resp.Body, &v), "version body decodes")
	assert.Equal(t, "1.54", v.APIVersion, "ApiVersion is served as given")
	assert.Equal(t, "1.40", v.MinAPIVersion, "MinAPIVersion is served as given")
}
