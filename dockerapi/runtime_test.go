package dockerapi_test

import (
	"context"
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tophergopher/mongotest/dockerapi"
	"github.com/tophergopher/mongotest/dockerclient"
	"github.com/tophergopher/mongotest/dockermock"
)

// Real GET /version bodies, trimmed to the fields that identify the daemon.
//
// Docker names its engine component "Engine" and puts a product string in
// Platform.Name. Podman names its component "Podman Engine" and puts
// goos/goarch/distro in Platform.Name, so Platform.Name alone cannot tell
// them apart and the component list is what to look at.
const (
	dockerVersionBody = `{
		"Platform":{"Name":"Docker Engine - Community"},
		"Components":[{"Name":"Engine","Version":"29.3.1"},{"Name":"containerd","Version":"1.7.27"}],
		"Version":"29.3.1","ApiVersion":"1.54","MinAPIVersion":"1.40"}`

	podman5VersionBody = `{
		"Platform":{"Name":"linux/amd64/fedora-40"},
		"Components":[{"Name":"Podman Engine","Version":"5.7.1"},{"Name":"Conmon","Version":"2.1.12"},{"Name":"OCI Runtime (crun)","Version":"1.15"}],
		"Version":"5.7.1","ApiVersion":"1.41","MinAPIVersion":"1.24"}`

	podman6VersionBody = `{
		"Platform":{"Name":"linux/arm64/debian-13"},
		"Components":[{"Name":"Podman Engine","Version":"6.0.0"}],
		"Version":"6.0.0","ApiVersion":"1.44","MinAPIVersion":"1.24"}`

	anonymousVersionBody = `{"ApiVersion":"1.44","MinAPIVersion":"1.24"}`
)

// versionDaemon serves one /version body and nothing else.
func versionDaemon(tb testing.TB, body string) *dockermock.Daemon {
	tb.Helper()
	d := dockermock.NewDaemon(dockermock.OverTCP())
	tb.Cleanup(d.Close)
	d.Handle(http.MethodGet, "/version", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(body))
	})
	return d
}

func TestClientReportsTheRuntimeItNegotiatedWith(t *testing.T) {
	for _, tc := range []struct {
		name    string
		body    string
		runtime dockerapi.Runtime
		version string
		product string
	}{
		{"docker", dockerVersionBody, dockerapi.RuntimeDocker, "1.44", "Docker Engine - Community"},
		{"podman 5.7, which caps at 1.41", podman5VersionBody, dockerapi.RuntimePodman, "1.41", "Podman Engine 5.7.1"},
		{"podman 6, which reports 1.44", podman6VersionBody, dockerapi.RuntimePodman, "1.44", "Podman Engine 6.0.0"},
		{"a daemon that identifies itself as neither", anonymousVersionBody, dockerapi.RuntimeUnknown, "1.44", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d := versionDaemon(t, tc.body)
			c, err := dockerapi.New(dockerapi.WithHost(d.Host()))
			require.NoError(t, err, "building a client against the fake daemon")
			require.NoError(t, c.Negotiate(context.Background()), "negotiation must succeed against %s", tc.name)

			assert.Equal(t, tc.runtime, c.Runtime(), "the runtime must be identified from the component list")
			assert.Equal(t, tc.version, c.APIVersion(), "the negotiated version must be the lower of the client's preference and the daemon's maximum")
			assert.Equal(t, tc.product, c.ServerProduct(), "the product string is what error messages name")
		})
	}
}

func TestPodmanNegotiatesEvenThoughItCapsBelowThePreferredVersion(t *testing.T) {
	// This is the whole reason Podman works here. Podman capped its
	// Docker-compatible API at 1.41 from 4.x through 5.7, raising it to
	// 1.44 only in 5.8. A client that refuses anything below its own
	// preferred version cannot talk to those releases at all; this one
	// floors at MinSupportedAPIVersion instead and negotiates down.
	require.Equal(t, "1.44", dockerapi.PreferredAPIVersion, "the preferred version is above Podman's cap, which is what makes this test meaningful")
	require.Equal(t, "1.24", dockerapi.MinSupportedAPIVersion, "the floor is what lets negotiation succeed")

	d := versionDaemon(t, podman5VersionBody)
	c, err := dockerapi.New(dockerapi.WithHost(d.Host()))
	require.NoError(t, err, "building a client against a Podman-shaped daemon")
	require.NoError(t, c.Negotiate(context.Background()), "a daemon capping at 1.41 must still negotiate")
	assert.Equal(t, "1.41", c.APIVersion(), "the daemon's maximum is used when it is below the preferred version")
}

func TestRuntimeIsUnknownBeforeNegotiation(t *testing.T) {
	d := versionDaemon(t, dockerVersionBody)
	c, err := dockerapi.New(dockerapi.WithHost(d.Host()))
	require.NoError(t, err, "building a client")
	assert.Equal(t, dockerapi.RuntimeUnknown, c.Runtime(), "nothing is known about the daemon until it has been asked")
	assert.Empty(t, c.ServerProduct(), "no product is known before negotiation either")
}

func TestVersionErrorNamesTheDaemonProduct(t *testing.T) {
	// A version mismatch is confusing enough without the message insisting
	// the user upgrade "the docker daemon" when they are running Podman.
	withProduct := &dockerclient.APIVersionError{
		Product:   "Podman Engine 5.7.1",
		ServerMax: "1.41", ClientMin: "1.44",
	}
	assert.Contains(t, withProduct.Error(), "Podman Engine 5.7.1", "the message must name the daemon that reported the window")
	assert.NotContains(t, withProduct.Error(), "the docker daemon", "naming the product replaces the generic wording")

	withoutProduct := &dockerclient.APIVersionError{ServerMax: "1.41", ClientMin: "1.44"}
	assert.Contains(t, withoutProduct.Error(), "the docker daemon", "the generic wording stays when the product is unknown")
}

func TestPodmanIsIdentifiedByItsResponseHeader(t *testing.T) {
	// Podman sets Libpod-API-Version on every versioned response and Docker
	// has no such header, which makes it a stronger signal than anything in
	// the body: it does not depend on which components happened to report
	// successfully, and it has been stable across Podman 4, 5 and 6.
	d := dockermock.NewDaemon(dockermock.OverTCP())
	t.Cleanup(d.Close)
	// A body that identifies nothing, so only the header can decide.
	d.Handle(http.MethodGet, "/version", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Libpod-API-Version", "5.8.0")
		w.Header().Set("Server", "Libpod/5.8.0 (linux)")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(anonymousVersionBody))
	})

	c, err := dockerapi.New(dockerapi.WithHost(d.Host()))
	require.NoError(t, err, "building a client")
	require.NoError(t, c.Negotiate(context.Background()), "negotiation against a header-only Podman")
	assert.Equal(t, dockerapi.RuntimePodman, c.Runtime(), "the Libpod-API-Version header identifies Podman on its own")
	assert.Contains(t, c.ServerProduct(), "5.8.0", "the libpod version from the header is used when the body says nothing")
}

func TestDockerIsNotMistakenForPodmanOnAVanillaBuild(t *testing.T) {
	// A moby build from source leaves Platform.Name empty, so a check that
	// looked for "docker" there would report Unknown. The component named
	// "Engine" is what identifies it.
	body := `{"Platform":{"Name":""},"Components":[{"Name":"Engine","Version":"30.0.0-dev"}],"ApiVersion":"1.51","MinAPIVersion":"1.24"}`
	d := versionDaemon(t, body)
	c, err := dockerapi.New(dockerapi.WithHost(d.Host()))
	require.NoError(t, err, "building a client")
	require.NoError(t, c.Negotiate(context.Background()), "negotiation against a vanilla moby build")
	assert.Equal(t, dockerapi.RuntimeDocker, c.Runtime(), "an Engine component is Docker even with no platform name")
	assert.Equal(t, "Engine 30.0.0-dev", c.ServerProduct(), "the component stands in when the platform name is empty")
}
