package mobyclient_test

import (
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/tophergopher/mongotest/dockerclient"
	"github.com/tophergopher/mongotest/dockerclient/dockerclienttest"
	"github.com/tophergopher/mongotest/dockermock"
	"github.com/tophergopher/mongotest/mobyclient"
)

// servePing registers the /_ping route the moby client negotiates the API
// version against on its first request. dockermock.Daemon's ServeDefaults
// and ServeFake only register the /version route dockerapi negotiates
// against, so every test in this package that talks to a fake daemon must
// add this route itself.
func servePing(d *dockermock.Daemon) {
	ping := func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Api-Version", dockermock.DefaultAPIVersion)
		w.Header().Set("Ostype", "linux")
		w.WriteHeader(http.StatusOK)
	}
	d.Handle(http.MethodHead, "/_ping", ping)
	d.Handle(http.MethodGet, "/_ping", ping)
}

// deNormalizeImageRef undoes the "docker.io/library/" (or "docker.io/")
// prefix the moby client adds to a bare repository name before it pulls, so
// this fake daemon's image-create route can key its store the same way a
// real daemon does: by the short name a caller both pulls and later
// inspects or creates a container from. A real daemon applies the same
// reference normalization on every request, not only on pull, so this
// asymmetry only exists because dockermock's Fake keys images by literal
// string.
func deNormalizeImageRef(ref string) string {
	for _, prefix := range []string{"docker.io/library/", "docker.io/"} {
		if rest, ok := strings.CutPrefix(ref, prefix); ok {
			return rest
		}
	}
	return ref
}

// serveNormalizedPull replaces the fake daemon's /images/create route (as
// registered by ServeFake) with one that first undoes the moby client's
// reference normalization, so a pull of "mongo:8" and a later
// ContainerCreate or ImageInspect by "mongo:8" agree on the same stored
// image, exactly as they would against a real daemon.
func serveNormalizedPull(d *dockermock.Daemon, f *dockermock.Fake) {
	d.Handle(http.MethodPost, "/images/create", func(w http.ResponseWriter, r *http.Request) {
		ref := r.URL.Query().Get("fromImage")
		if tag := r.URL.Query().Get("tag"); tag != "" {
			ref += ":" + tag
		}
		ref = deNormalizeImageRef(ref)
		if err := f.ImagePull(r.Context(), ref); err != nil {
			dockermock.JSON(w, http.StatusOK, map[string]any{
				"error": err.Error(), "errorDetail": map[string]string{"message": err.Error()},
			})
			return
		}
		fmt.Fprintln(w, `{"status":"Status: Downloaded newer image for `+ref+`"}`)
	})
}

// newConformanceClient points a real mobyclient at a fake daemon whose
// routes are backed by an in-memory store, so the suite exercises the whole
// HTTP path while still asserting on state that actually changes.
func newConformanceClient(tb testing.TB) dockerclient.Client {
	tb.Helper()
	d := dockermock.NewDaemon()
	tb.Cleanup(d.Close)
	f := dockermock.NewFake()
	d.ServeFake(f)
	servePing(d)
	serveNormalizedPull(d, f)
	c, err := mobyclient.New(mobyclient.WithHost(d.Host()))
	require.NoError(tb, err, "a client against the fake daemon must construct")
	return c
}

// TestConformance holds this client to the behaviour every implementation
// shares, so swapping it for dockerapi or a double changes nothing a caller
// can observe.
func TestConformance(t *testing.T) {
	dockerclienttest.Conformance(t, func(t *testing.T) dockerclient.Client {
		return newConformanceClient(t)
	})
}

// BenchmarkClient measures the calls in the interface on the same scale as
// every other implementation.
func BenchmarkClient(b *testing.B) {
	dockerclienttest.Benchmarks(b, func(b *testing.B) dockerclient.Client {
		return newConformanceClient(b)
	})
}
