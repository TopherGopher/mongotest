package dockerclient_test

import (
	"errors"
	"io"
	"io/fs"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tophergopher/mongotest/dockerclient"
)

// The validators and error carriers here are shared by every implementation
// of Client, so these tests pin the behaviour all of them inherit.

func TestParseImageRef(t *testing.T) {
	good := []struct{ ref, name, tag, digest string }{
		{"mongo", "mongo", "latest", ""},
		{"mongo:8", "mongo", "8", ""},
		{"mongo:8.0-noble", "mongo", "8.0-noble", ""},
		{"library/mongo:8", "library/mongo", "8", ""},
		{"docker.io/library/mongo:8", "docker.io/library/mongo", "8", ""},
		{"localhost:5000/mongo", "localhost:5000/mongo", "latest", ""},
		{"localhost:5000/team/mongo:8.0", "localhost:5000/team/mongo", "8.0", ""},
		{"GHCR.io/org-name/img:v1", "GHCR.io/org-name/img", "v1", ""}, // uppercase allowed in the registry host only
		{"mongo:8/json", "mongo:8/json", "latest", ""},                // legal: host "mongo:8", repository "json"
		{"[::1]:5000/mongo", "[::1]:5000/mongo", "latest", ""},
		{"mongo@sha256:" + strings.Repeat("a", 64), "mongo", "", "sha256:" + strings.Repeat("a", 64)},
		{"mongo:8@sha256:" + strings.Repeat("0", 64), "mongo", "8", "sha256:" + strings.Repeat("0", 64)},
		{"my__image.name-x", "my__image.name-x", "latest", ""},
	}
	for _, tc := range good {
		r, err := dockerclient.ParseImageRef(tc.ref)
		require.NoError(t, err, "%q is a valid reference", tc.ref)
		assert.Equal(t, tc.name, r.Name, "%q: repository part", tc.ref)
		assert.Equal(t, tc.tag, r.Tag, "%q: tag (latest when absent and no digest)", tc.ref)
		assert.Equal(t, tc.digest, r.Digest, "%q: digest", tc.ref)
	}
	bad := []string{
		"", " ", "Mongo", "mongo:8:9", "mongo:", ":8", "mongo/", "/mongo", "mongo//x", "mongo:8 ", "mongo:-8",
		"mongo@sha256:short", "mongo@notadigest", "mon go", "mongo:" + strings.Repeat("t", 129),
		strings.Repeat("a", 256), "mongo?x=1", "mongo#1", "ghcr.io/Org-Name/img:v1",
	}
	for _, ref := range bad {
		_, err := dockerclient.ParseImageRef(ref)
		require.ErrorIs(t, err, dockerclient.ErrInvalidArgument, "%q must be rejected as an invalid argument", ref)
		assert.Contains(t, err.Error(), "repository", "%q: the error must explain the reference grammar", ref)
	}
	_, err := dockerclient.ParseImageRef("Mongo:8")
	assert.Contains(t, err.Error(), "lowercase", "an uppercase repository gets the daemon's familiar wording")
}

func TestCheckID(t *testing.T) {
	for _, in := range []string{"abc123", "  abc123 ", "/mongotest-1", "mongotest_1.x-y", "de686f1e8de9bcabdc4b20b2d9b80d33f90ed79aa7588ad907d953ad63dc9d3c"} {
		got, err := dockerclient.CheckID("container", in)
		require.NoError(t, err, "%q is an acceptable id", in)
		assert.NotEmpty(t, got, "%q: a cleaned id is returned", in)
		assert.False(t, strings.ContainsAny(got, " /"), "%q: whitespace and the leading slash are stripped, got %q", in, got)
	}
	for _, in := range []string{"", "   ", "a/b", "../x", "abc?force=1", "abc#", "-abc", ".abc", "a b", "a\nb"} {
		_, err := dockerclient.CheckID("container", in)
		require.ErrorIs(t, err, dockerclient.ErrInvalidArgument, "%q must be rejected so it cannot alter the URL path", in)
	}
	_, err := dockerclient.CheckID("exec", "")
	assert.Contains(t, err.Error(), "exec id", "the error must name the kind of id that was empty")
	assert.Contains(t, err.Error(), "ExecCreate", "the error must say where a valid id comes from")
}

func TestValidateContainerConfig(t *testing.T) {
	ok := dockerclient.ContainerConfig{
		Image:        "mongo:8",
		Labels:       map[string]string{"mongotest": "regression"},
		ExposedPorts: map[string]struct{}{"27017/tcp": {}, "27018": {}},
		HostConfig: &dockerclient.HostConfig{PortBindings: map[string][]dockerclient.PortBinding{
			"27017/tcp": {{HostIP: "127.0.0.1", HostPort: "34819"}, {HostIP: "", HostPort: ""}, {HostPort: "40000-40010"}},
			"53/udp":    {{HostIP: "::1", HostPort: "5353"}},
		}},
	}
	require.NoError(t, ok.Validate(), "a config with valid ports, bindings and labels must pass")
	bad := map[string]dockerclient.ContainerConfig{
		"no image":          {},
		"bad image":         {Image: "Mongo"},
		"empty label key":   {Image: "mongo", Labels: map[string]string{"": "x"}},
		"exposed no number": {Image: "mongo", ExposedPorts: map[string]struct{}{"tcp": {}}},
		"exposed port zero": {Image: "mongo", ExposedPorts: map[string]struct{}{"0/tcp": {}}},
		"exposed too big":   {Image: "mongo", ExposedPorts: map[string]struct{}{"70000/tcp": {}}},
		"bad proto":         {Image: "mongo", ExposedPorts: map[string]struct{}{"27017/icmp": {}}},
		"binding key":       {Image: "mongo", HostConfig: &dockerclient.HostConfig{PortBindings: map[string][]dockerclient.PortBinding{"x/tcp": {{}}}}},
		"binding host ip":   {Image: "mongo", HostConfig: &dockerclient.HostConfig{PortBindings: map[string][]dockerclient.PortBinding{"27017/tcp": {{HostIP: "localhost"}}}}},
		"binding host port": {Image: "mongo", HostConfig: &dockerclient.HostConfig{PortBindings: map[string][]dockerclient.PortBinding{"27017/tcp": {{HostPort: "abc"}}}}},
		"binding range":     {Image: "mongo", HostConfig: &dockerclient.HostConfig{PortBindings: map[string][]dockerclient.PortBinding{"27017/tcp": {{HostPort: "40010-40000"}}}}},
		"empty cmd element": {Image: "mongo", Cmd: []string{"--replSet", ""}},
	}
	for name, cfg := range bad {
		err := cfg.Validate()
		require.ErrorIs(t, err, dockerclient.ErrInvalidArgument, "%s: must be rejected as an invalid argument", name)
		var ia *dockerclient.InvalidArgumentError
		require.True(t, errors.As(err, &ia), "%s: the typed error must be extractable", name)
		assert.NotEmpty(t, ia.Fix, "%s: every validation error must say what to do", name)
	}
}

func TestInvalidArgumentErrorMessage(t *testing.T) {
	err := dockerclient.InvalidArgument("container id", "a/b", "only letters are allowed", "pass the id from ContainerCreate")
	assert.ErrorIs(t, err, dockerclient.ErrInvalidArgument, "every InvalidArgumentError matches the sentinel")
	assert.Equal(t, `docker: invalid container id "a/b": only letters are allowed. pass the id from ContainerCreate`, err.Error(), "the message follows argument, value, problem, fix")
	var ia *dockerclient.InvalidArgumentError
	require.True(t, errors.As(err, &ia), "callers can extract the typed error")
	assert.Equal(t, "a/b", ia.Value, "the offending value is available to callers")
	assert.Same(t, dockerclient.ErrNoCommand, dockerclient.ErrNoCommand, "predeclared argument errors are comparable values")
	assert.ErrorIs(t, dockerclient.ErrNoCommand, dockerclient.ErrInvalidArgument, "predeclared argument errors also match the sentinel")
}

func TestConnectionErrorWrapping(t *testing.T) {
	err := dockerclient.WrapConnectionError("unix:///var/run/docker.sock", fs.ErrPermission)
	require.ErrorIs(t, err, dockerclient.ErrConnectionFailed, "permission denied on the socket is a connection failure")
	assert.Contains(t, err.Error(), "permission denied", "the problem must be stated")
	assert.Contains(t, err.Error(), "docker group", "the fix must mention the docker group")
	var ce *dockerclient.ConnectionError
	require.True(t, errors.As(err, &ce), "callers can extract the typed error")
	assert.Equal(t, "unix:///var/run/docker.sock", ce.Host, "the host is carried on the error")

	err = dockerclient.WrapConnectionError("unix:///x.sock", fs.ErrNotExist)
	assert.ErrorIs(t, err, dockerclient.ErrConnectionFailed, "a missing socket is a connection failure")
	assert.Contains(t, err.Error(), "does not exist", "the problem must be stated")
	assert.Contains(t, err.Error(), "Start the docker daemon", "the fix must say to start the daemon")

	assert.Nil(t, dockerclient.WrapConnectionError("h", nil), "nil stays nil")
	plain := errors.New("something else")
	assert.Same(t, plain, dockerclient.WrapConnectionError("h", plain), "errors that are not connection problems pass through unchanged")
}

func TestAPIVersionErrorMessages(t *testing.T) {
	feature := &dockerclient.APIVersionError{Feature: "ExecConfig.WorkingDir", Required: "1.35", Negotiated: "1.30"}
	assert.ErrorIs(t, feature, dockerclient.ErrAPIVersion, "feature gates match the sentinel")
	assert.Contains(t, feature.Error(), "1.35", "the required version is named")
	assert.Contains(t, feature.Error(), "1.30", "the negotiated version is named")
	assert.Contains(t, feature.Error(), "drop the ExecConfig.WorkingDir option", "the fix is stated")
}

func TestResponseErrorMessage(t *testing.T) {
	err := dockerclient.DecodeError("GET", "/containers/x/json", io.ErrUnexpectedEOF)
	assert.ErrorIs(t, err, dockerclient.ErrDaemonResponse, "decode failures match the sentinel")
	assert.ErrorIs(t, err, io.ErrUnexpectedEOF, "the underlying decode error is wrapped")
	assert.Contains(t, err.Error(), "DOCKER_API_VERSION", "the message points at the version pin as the likely cause")
	assert.Contains(t, dockerclient.UnexpectedStatus("POST", "/x", 202).Error(), "unexpected status 202", "unexpected statuses are named")
}
