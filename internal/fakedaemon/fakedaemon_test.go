package fakedaemon

import (
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"strings"
	"testing"
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
		req, _ := http.NewRequest("GET", "http://api.moby.localhost"+p+"?size=1", strings.NewReader("hello"))
		resp, err := c.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		var got map[string]string
		_ = json.NewDecoder(resp.Body).Decode(&got)
		resp.Body.Close()
		if resp.StatusCode != 200 || got["Id"] != "abc" {
			t.Fatalf("%s: status %d body %v", p, resp.StatusCode, got)
		}
	}
	reqs := s.Requests()
	if len(reqs) != 2 {
		t.Fatalf("recorded %d requests, want 2", len(reqs))
	}
	if reqs[1].Path != "/containers/abc/json" || reqs[1].RawPath != "/v1.44/containers/abc/json" {
		t.Fatalf("paths: %+v", reqs[1])
	}
	if string(reqs[1].Body) != "hello" || reqs[1].Query.Get("size") != "1" || reqs[1].Method != "GET" {
		t.Fatalf("record: %+v", reqs[1])
	}
}

func TestUnmatchedIs404JSON(t *testing.T) {
	s := NewTCP(t)
	resp, err := http.Get(s.URL() + "/v1.41/nope")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != 404 || strings.TrimSpace(string(b)) != `{"message":"page not found"}` {
		t.Fatalf("status %d body %q", resp.StatusCode, b)
	}
	// Method must match too.
	s.Handle("POST", "/x", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(204) })
	resp2, _ := http.Get(s.URL() + "/x")
	resp2.Body.Close()
	if resp2.StatusCode != 404 {
		t.Fatalf("GET on POST route: %d", resp2.StatusCode)
	}
}
