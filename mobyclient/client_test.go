package mobyclient

import (
	"archive/tar"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"net/netip"
	"strings"
	"testing"

	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/api/types/network"
	mobydocker "github.com/moby/moby/client"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	cerrdefs "github.com/containerd/errdefs"

	"github.com/tophergopher/mongotest/dockerclient"
	"github.com/tophergopher/mongotest/dockermock"
)

// --- construction and options ----------------------------------------------

func TestNewRequiresValidHost(t *testing.T) {
	_, err := New(WithHost("bogus"))
	require.ErrorIs(t, err, dockerclient.ErrInvalidArgument, "a host without a scheme must be rejected as an invalid argument")
	var ia *dockerclient.InvalidArgumentError
	require.ErrorAs(t, err, &ia, "the error must carry the argument details")
	assert.Equal(t, "bogus", ia.Value, "the rejected value must be reported back")
}

func TestNewWithoutHostUsesMobyDefault(t *testing.T) {
	c, err := New()
	require.NoError(t, err, "New with no options must construct against the moby client's own default host")
	assert.Equal(t, mobydocker.DefaultDockerHost, c.Host(), "with no WithHost, mobyclient must use the moby client's platform default, not perform its own discovery")
}

func TestWithHostOverridesDefault(t *testing.T) {
	c, err := New(WithHost("tcp://build-host:2376"))
	require.NoError(t, err, "a well-formed tcp host must construct")
	assert.Equal(t, "tcp://build-host:2376", c.Host(), "Host must report exactly the address WithHost set")
}

func TestWithMobyClientWrapsExistingClient(t *testing.T) {
	moby, err := mobydocker.New(mobydocker.WithHost("tcp://already-built:2376"))
	require.NoError(t, err, "constructing the moby client directly")
	c, err := New(WithMobyClient(moby))
	require.NoError(t, err, "wrapping an existing moby client must succeed")
	assert.Equal(t, moby.DaemonHost(), c.Host(), "Host must report the wrapped client's own daemon host")
}

func TestWithMobyClientRejectsNil(t *testing.T) {
	_, err := New(WithMobyClient(nil))
	require.ErrorIs(t, err, dockerclient.ErrInvalidArgument, "a nil moby client must be rejected as an invalid argument, not accepted and panic later")
}

// recordLogger is a dockerclient.Logger that records every Debug call, for
// asserting WithLogger is actually wired to the client's requests.
type recordLogger struct{ entries []logEntry }

type logEntry struct {
	msg string
	kv  map[string]any
}

func (r *recordLogger) Debug(msg string, kv ...any) {
	m := map[string]any{}
	for i := 0; i+1 < len(kv); i += 2 {
		key, _ := kv[i].(string)
		m[key] = kv[i+1]
	}
	r.entries = append(r.entries, logEntry{msg: msg, kv: m})
}
func (*recordLogger) Info(string, ...any)  {}
func (*recordLogger) Warn(string, ...any)  {}
func (*recordLogger) Error(string, ...any) {}

func TestWithLoggerReceivesRequestRecords(t *testing.T) {
	fd, f := newTestDaemon(t)
	rec := &recordLogger{}
	c, err := New(WithHost(fd.Host()), WithLogger(rec))
	require.NoError(t, err, "client construction with a logger")

	_, err = c.ImageInspect(context.Background(), "mongo:8")
	require.ErrorIs(t, err, dockerclient.ErrNotFound, "the fake daemon starts empty, so this image is not found")
	_ = f

	require.GreaterOrEqual(t, len(rec.entries), 1, "at least one debug record is expected for the request that was made")
	last := rec.entries[len(rec.entries)-1]
	assert.Equal(t, "docker request", last.msg, "request records must use a fixed message so log filters can match it")
	assert.Equal(t, http.MethodGet, last.kv["method"], "the record must carry the HTTP method")
	assert.Contains(t, fmt.Sprint(last.kv["path"]), "/images/", "the record must carry the request path")
}

func TestWithLoggerDefaultsToNopLogger(t *testing.T) {
	fd, _ := newTestDaemon(t)
	c, err := New(WithHost(fd.Host()))
	require.NoError(t, err, "client construction without WithLogger")
	assert.NotNil(t, c.logger, "the default logger must never be nil, so ImageInspect and friends can call it unconditionally")
}

// --- error mapping -----------------------------------------------------------

func TestMapErrorNil(t *testing.T) {
	assert.NoError(t, mapError("host", http.MethodGet, "/x", nil), "a nil error must map to nil, not a wrapped nil")
}

func TestMapErrorContextPassesThrough(t *testing.T) {
	got := mapError("host", http.MethodGet, "/x", context.Canceled)
	assert.True(t, got == context.Canceled, "a context.Canceled error must be returned unchanged so callers can compare it directly, got %v", got)

	got = mapError("host", http.MethodGet, "/x", context.DeadlineExceeded)
	assert.True(t, got == context.DeadlineExceeded, "a context.DeadlineExceeded error must be returned unchanged, got %v", got)
}

func TestMapErrorClassifiesDaemonErrors(t *testing.T) {
	tests := []struct {
		name       string
		err        error
		wantStatus int
		wantSent   error
		wantMsg    string
	}{
		{
			name:       "not found",
			err:        fmt.Errorf("Error response from daemon: %w", cerrdefs.ErrNotFound.WithMessage("No such container: x")),
			wantStatus: http.StatusNotFound,
			wantSent:   dockerclient.ErrNotFound,
			wantMsg:    "No such container: x",
		},
		{
			name:       "conflict",
			err:        fmt.Errorf("Error response from daemon: %w", cerrdefs.ErrConflict.WithMessage("name already in use")),
			wantStatus: http.StatusConflict,
			wantSent:   dockerclient.ErrConflict,
			wantMsg:    "name already in use",
		},
		{
			name:       "unauthenticated maps to ErrUnauthorized at 401",
			err:        fmt.Errorf("Error response from daemon: %w", cerrdefs.ErrUnauthenticated.WithMessage("authentication required")),
			wantStatus: http.StatusUnauthorized,
			wantSent:   dockerclient.ErrUnauthorized,
			wantMsg:    "authentication required",
		},
		{
			name:       "permission denied maps to ErrUnauthorized at 403",
			err:        fmt.Errorf("Error response from daemon: %w", cerrdefs.ErrPermissionDenied.WithMessage("forbidden")),
			wantStatus: http.StatusForbidden,
			wantSent:   dockerclient.ErrUnauthorized,
			wantMsg:    "forbidden",
		},
		{
			// dockerclient.StatusError.Is only matches ErrNotFound (404),
			// ErrConflict (409) and ErrUnauthorized (401/403): a daemon-side
			// 400 is not ErrInvalidArgument, which is reserved for values
			// this client itself refused before sending anything. A 400 is
			// still fully inspectable as a StatusError.
			name:       "invalid argument (daemon-side 400 has no sentinel of its own)",
			err:        fmt.Errorf("Error response from daemon: %w", cerrdefs.ErrInvalidArgument.WithMessage("bad request")),
			wantStatus: http.StatusBadRequest,
			wantSent:   nil,
			wantMsg:    "bad request",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := mapError("unix:///var/run/docker.sock", http.MethodGet, "/containers/x/json", tc.err)
			if tc.wantSent != nil {
				require.ErrorIs(t, got, tc.wantSent, "%s must match its dockerclient sentinel", tc.name)
			} else {
				assert.False(t, errors.Is(got, dockerclient.ErrInvalidArgument), "%s must not be mistaken for a client-side InvalidArgumentError", tc.name)
			}
			var se *dockerclient.StatusError
			require.ErrorAs(t, got, &se, "%s must produce a StatusError callers can inspect", tc.name)
			assert.Equal(t, tc.wantStatus, se.StatusCode, "%s must carry the matching HTTP status", tc.name)
			assert.Equal(t, tc.wantMsg, se.Message, "the daemon's message must survive with the wrapping prefix stripped, not moby's own decoration")
			assert.Equal(t, http.MethodGet, se.Method, "the method describing the failed request must be preserved")
			assert.Equal(t, "/containers/x/json", se.Path, "the path describing the failed request must be preserved")
		})
	}
}

func TestMapErrorWrapsConnectionFailures(t *testing.T) {
	// A dial failure the way the moby client's own request path decorates
	// it: wrapped, never replaced, so the underlying cause is still there
	// for WrapConnectionError to find.
	cause := fmt.Errorf("error during connect: %w", fs.ErrNotExist)
	got := mapError("unix:///nonexistent/docker.sock", http.MethodGet, "/version", cause)
	require.ErrorIs(t, got, dockerclient.ErrConnectionFailed, "a wrapped fs.ErrNotExist must be classified as a connection failure")
	var ce *dockerclient.ConnectionError
	require.ErrorAs(t, got, &ce, "the mapped error must be inspectable as a ConnectionError")
	assert.Equal(t, "unix:///nonexistent/docker.sock", ce.Host, "the daemon address must be reported so the message names it")
}

func TestMapErrorRecoversExecUpgradeStatus(t *testing.T) {
	// What the moby client returns verbatim when an exec start does not get
	// the 101 upgrade it asked for; not classified by containerd/errdefs.
	got := mapError("unix:///var/run/docker.sock", http.MethodPost, "/exec/missing/start",
		errors.New("unable to upgrade to tcp, received 404"))
	require.ErrorIs(t, got, dockerclient.ErrNotFound, "a 404 recovered from the upgrade-failure message must still match ErrNotFound")
	var se *dockerclient.StatusError
	require.ErrorAs(t, got, &se, "it must be a StatusError like every other classified daemon response")
	assert.Equal(t, http.StatusNotFound, se.StatusCode, "the status code must be the one named in the moby client's message")
}

func TestMapErrorFallsBackToResponseError(t *testing.T) {
	got := mapError("unix:///var/run/docker.sock", http.MethodGet, "/containers/x/json", errors.New("something moby-shaped but unclassified"))
	require.ErrorIs(t, got, dockerclient.ErrDaemonResponse, "an error containerd/errdefs and WrapConnectionError do not recognise must still be a usable dockerclient error")
	var re *dockerclient.ResponseError
	require.ErrorAs(t, got, &re, "the fallback must be a ResponseError, never a bare, unwrapped error")
}

func TestWrapConstructError(t *testing.T) {
	assert.NoError(t, wrapConstructError("host", nil), "a nil error must map to nil")
	err := wrapConstructError("bogus", errors.New(`unable to parse docker host "bogus"`))
	require.ErrorIs(t, err, dockerclient.ErrInvalidArgument, "a host the moby client could not parse must be reported as invalid input, not left as an opaque construction failure")
}

// --- container.Config / HostConfig translation ------------------------------

func TestToMobyConfigTranslatesPortsAndHostConfig(t *testing.T) {
	cfg := dockerclient.ContainerConfig{
		Image:        "mongo:8",
		Cmd:          []string{"--replSet", "rs0"},
		Env:          []string{"TERM=dumb"},
		Labels:       map[string]string{"mongotest": "regression"},
		ExposedPorts: map[string]struct{}{"27017/tcp": {}},
		Tty:          true,
		OpenStdin:    true,
		HostConfig: &dockerclient.HostConfig{
			AutoRemove: true,
			PortBindings: map[string][]dockerclient.PortBinding{
				"27017/tcp": {{HostIP: "127.0.0.1", HostPort: "34819"}, {HostIP: "", HostPort: ""}},
			},
		},
	}
	mcfg, hostCfg, err := toMobyConfig(cfg)
	require.NoError(t, err, "a well-formed config must translate without error")

	assert.Equal(t, "mongo:8", mcfg.Image, "Image must round-trip unchanged")
	assert.Equal(t, []string{"--replSet", "rs0"}, mcfg.Cmd, "Cmd must round-trip unchanged")
	assert.Equal(t, []string{"TERM=dumb"}, mcfg.Env, "Env must round-trip unchanged")
	assert.Equal(t, cfg.Labels, mcfg.Labels, "Labels must round-trip unchanged")
	assert.True(t, mcfg.Tty, "Tty must round-trip")
	assert.True(t, mcfg.OpenStdin, "OpenStdin must round-trip")

	port, err := network.ParsePort("27017/tcp")
	require.NoError(t, err, "parsing the port key used in the assertion itself")
	_, ok := mcfg.ExposedPorts[port]
	assert.True(t, ok, "ExposedPorts must carry the 27017/tcp entry under moby's typed Port key")

	require.NotNil(t, hostCfg, "a HostConfig must be built when the input has one")
	assert.True(t, hostCfg.AutoRemove, "AutoRemove must round-trip")
	bindings := hostCfg.PortBindings[port]
	require.Len(t, bindings, 2, "both port bindings must be translated")
	assert.Equal(t, "127.0.0.1", bindings[0].HostIP.String(), "a set HostIP must parse to the matching netip.Addr")
	assert.Equal(t, "34819", bindings[0].HostPort, "HostPort must round-trip unchanged")
	assert.False(t, bindings[1].HostIP.IsValid(), "an empty HostIP must translate to the zero netip.Addr, not a parse error")
}

func TestToMobyConfigNilHostConfig(t *testing.T) {
	mcfg, hostCfg, err := toMobyConfig(dockerclient.ContainerConfig{Image: "mongo:8"})
	require.NoError(t, err, "a config with no HostConfig must still translate")
	assert.Equal(t, "mongo:8", mcfg.Image, "Image must still be set")
	assert.Nil(t, hostCfg, "a nil HostConfig must stay nil, not become an empty struct that changes daemon defaults")
}

// --- ContainerInspect translation -------------------------------------------

func TestToDockerclientInspect(t *testing.T) {
	port, err := network.ParsePort("27017/tcp")
	require.NoError(t, err, "parsing the port key used to build the fixture")
	ci := container.InspectResponse{
		ID:   "c0ffee1234ab",
		Name: "/mongotest-example",
		State: &container.State{
			Status:   "running",
			Running:  true,
			ExitCode: 0,
		},
		Config: &container.Config{Image: "mongo:8", Labels: map[string]string{"mongotest": "regression"}},
		NetworkSettings: &container.NetworkSettings{Ports: network.PortMap{
			port: {{HostIP: netip.MustParseAddr("127.0.0.1"), HostPort: "34819"}, {HostPort: ""}},
		}},
	}

	got := toDockerclientInspect(ci)
	assert.Equal(t, "c0ffee1234ab", got.ID, "ID must round-trip")
	assert.Equal(t, "/mongotest-example", got.Name, "Name must round-trip with its leading slash")
	assert.Equal(t, dockerclient.ContainerState{Status: "running", Running: true, ExitCode: 0}, got.State, "State fields must round-trip")
	assert.Equal(t, dockerclient.InspectedConfig{Image: "mongo:8", Labels: map[string]string{"mongotest": "regression"}}, got.Config, "Config must round-trip")
	assert.Equal(t, "34819", got.HostPort("27017/tcp"), "HostPort must find the published port")
	assert.Equal(t, "", got.HostPort("80/tcp"), "HostPort must be empty for a port that was never published")

	bindings := got.NetworkSettings.Ports["27017/tcp"]
	require.Len(t, bindings, 2, "both bindings for the port must be translated")
	assert.Equal(t, "127.0.0.1", bindings[0].HostIP, "a valid HostIP must be rendered as a plain string")
	assert.Equal(t, "", bindings[1].HostIP, "an unset (zero) HostIP must render as the empty string, not netip's \"invalid IP\"")
}

func TestToDockerclientInspectNilSections(t *testing.T) {
	got := toDockerclientInspect(container.InspectResponse{ID: "c1", Name: "/c1"})
	assert.Equal(t, "c1", got.ID, "ID must still be set")
	assert.Zero(t, got.State, "a nil State must translate to the zero ContainerState, not panic")
	assert.Zero(t, got.Config, "a nil Config must translate to the zero InspectedConfig, not panic")
	assert.Nil(t, got.NetworkSettings.Ports, "a nil NetworkSettings must translate to nil Ports, not panic")
}

// --- writeTar ----------------------------------------------------------------

func TestWriteTarProducesTheGivenFiles(t *testing.T) {
	files := []dockerclient.File{
		{Name: "mongo-tls/server.pem", Mode: 0o640, Content: []byte("cert")},
		{Name: "mongo-tls/ca.pem", Content: []byte("ca")}, // Mode 0 must default to 0o644
	}
	var buf bytes.Buffer
	require.NoError(t, writeTar(&buf, files), "writing a well-formed file list must not error")

	entries := readTar(t, buf.Bytes())
	require.Contains(t, entries, "mongo-tls/", "a directory entry must be written for the shared parent directory")
	require.Contains(t, entries, "mongo-tls/server.pem", "the first file must be present")
	require.Contains(t, entries, "mongo-tls/ca.pem", "the second file must be present")
	assert.Equal(t, "cert", string(entries["mongo-tls/server.pem"].body), "file content must round-trip exactly")
	assert.Equal(t, int64(0o640), entries["mongo-tls/server.pem"].mode, "an explicit mode must round-trip")
	assert.Equal(t, int64(0o644), entries["mongo-tls/ca.pem"].mode, "a zero Mode must default to 0o644, matching dockerapi")
}

type tarEntry struct {
	mode int64
	body []byte
}

// readTar decodes a tar archive built by writeTar into a map keyed by entry
// name, for asserting on the files and directories it produced.
func readTar(t *testing.T, data []byte) map[string]tarEntry {
	t.Helper()
	tr := tar.NewReader(bytes.NewReader(data))
	out := map[string]tarEntry{}
	for {
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			return out
		}
		require.NoError(t, err, "reading a tar entry written by writeTar")
		body, err := io.ReadAll(tr)
		require.NoError(t, err, "reading the body of tar entry %s", hdr.Name)
		out[hdr.Name] = tarEntry{mode: hdr.Mode, body: body}
	}
}

// --- test daemon helper -------------------------------------------------

// newTestDaemon starts a dockermock.Daemon backed by a fresh dockermock.Fake,
// with the /_ping route the moby client negotiates the API version against
// on its first request. dockermock.Daemon's ServeFake only registers the
// /version route dockerapi negotiates against.
func newTestDaemon(t *testing.T) (*dockermock.Daemon, *dockermock.Fake) {
	t.Helper()
	d := dockermock.NewDaemon()
	t.Cleanup(d.Close)
	f := dockermock.NewFake()
	d.ServeFake(f)
	ping := func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Api-Version", dockermock.DefaultAPIVersion)
		w.WriteHeader(http.StatusOK)
	}
	d.Handle(http.MethodHead, "/_ping", ping)
	d.Handle(http.MethodGet, "/_ping", ping)
	return d, f
}

// uncomparableError is a struct error type carrying a slice, which makes it
// uncomparable with ==. Errors like this are legal and the moby client is
// free to return one, so nothing on the mapping path may compare error
// values with == .
type uncomparableError struct{ frames []string }

func (e uncomparableError) Error() string { return strings.Join(e.frames, ": ") }

func TestMapErrorSurvivesAnUncomparableError(t *testing.T) {
	// Comparing two interface values whose dynamic type is identical and
	// uncomparable panics at run time. mapError must classify by type, not
	// by value identity, or an unusual error from the moby client takes the
	// whole test process down with it.
	err := uncomparableError{frames: []string{"something", "went wrong"}}
	require.NotPanics(t, func() {
		mapped := mapError("unix:///var/run/docker.sock", http.MethodGet, "/containers/c1/json", err)
		require.Error(t, mapped, "an unclassifiable error is still an error")
		assert.ErrorIs(t, mapped, dockerclient.ErrDaemonResponse,
			"an error that is neither a daemon status nor a connection failure is a response error")
		// errors.Is cannot match an uncomparable target by value, so the
		// chain is checked with errors.As instead.
		var original uncomparableError
		assert.True(t, errors.As(mapped, &original), "the original error stays in the chain")
	}, "mapError must not compare error values with ==")
}
