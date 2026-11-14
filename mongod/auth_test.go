package mongod_test

import (
	"archive/tar"
	"context"
	"errors"
	"fmt"
	"io"
	"net/url"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/tophergopher/mongotest/dockerclient"
	"github.com/tophergopher/mongotest/dockermock"
	"github.com/tophergopher/mongotest/mongod"
)

// siblingGateway is an address that is not loopback, which makes Start
// publish on every interface.
const siblingGateway = "172.17.0.1"

// createdFor returns the create request a set of options produces. The
// double never reports a mongod, so the start ends on its timeout: the
// request is built before anything is waited for, and nothing has to listen
// on an address this machine does not have.
func createdFor(t *testing.T, opts ...*mongod.Options) dockerclient.ContainerConfig {
	t.Helper()
	m := &dockermock.Mock{
		ContainerCreateFunc: func(context.Context, string, dockerclient.ContainerConfig) (string, []string, error) {
			return "3f1a9c4b7e2d5a8f0b6c3e9d1a4f7b2c5e8d0a3f6b9c2e5d8a1f4b7c0e3d6a9f", nil, nil
		},
		ContainerInspectFunc: func(_ context.Context, id string) (dockerclient.ContainerInspect, error) {
			return inspectPublishing(id, 1), nil
		},
	}
	opts = append(opts, mongod.WithDocker(m).WithStartTimeout(20*time.Millisecond))
	_, err := mongod.Start(context.Background(), opts...)
	require.ErrorIs(t, err, mongod.ErrNotReady, "this double never reports a mongod")
	return createdConfig(t, m)
}

// rootUser returns the root user a create request tells the image to
// create, or "" and "" if it asks for none. Images read one of two spellings
// of the variables, so both must be set and must agree.
func rootUser(t *testing.T, cfg dockerclient.ContainerConfig) (username, password string) {
	t.Helper()
	env := map[string]string{}
	for _, entry := range cfg.Env {
		key, value, _ := strings.Cut(entry, "=")
		env[key] = value
	}
	assert.Equal(t, env["MONGO_INITDB_ROOT_USERNAME"], env["MONGODB_INITDB_ROOT_USERNAME"], "the official image and Atlas Local read different variables, so both carry the username")
	assert.Equal(t, env["MONGO_INITDB_ROOT_PASSWORD"], env["MONGODB_INITDB_ROOT_PASSWORD"], "and both carry the password")
	return env["MONGO_INITDB_ROOT_USERNAME"], env["MONGO_INITDB_ROOT_PASSWORD"]
}

// parseURI parses a container's connection string.
func parseURI(t *testing.T, c *mongod.Container) *url.URL {
	t.Helper()
	parsed, err := url.Parse(c.URI())
	require.NoError(t, err, "the URI must be one a driver can parse")
	return parsed
}

// flagValue returns the argument after a flag in a container command.
func flagValue(cmd []string, flag string) (string, bool) {
	i := slices.Index(cmd, flag)
	if i < 0 || i+1 >= len(cmd) {
		return "", false
	}
	return cmd[i+1], true
}

func TestLoopbackStartsWithoutCredentials(t *testing.T) {
	m, port := readyMock(t)

	c, err := mongod.Start(context.Background(), mongod.WithDocker(m).WithPort(port))
	require.NoError(t, err, "a default start succeeds")
	t.Cleanup(func() { _ = c.Stop(context.Background()) })

	username, _ := rootUser(t, createdConfig(t, m))
	assert.Empty(t, username, "nothing off this machine can reach a loopback port, so no user is created")
	assert.Empty(t, c.Username(), "the container reports no user")
	assert.Nil(t, parseURI(t, c).User, "and the URI carries none")
}

func TestWithAuthCreatesTheRootUserAndPutsItInTheURI(t *testing.T) {
	m, port := authMock(t)

	c, err := mongod.Start(context.Background(), mongod.WithDocker(m).WithPort(port).WithAuth("admin", "s3cret"))
	require.NoError(t, err, "credentials do not stop a start")
	t.Cleanup(func() { _ = c.Stop(context.Background()) })

	username, password := rootUser(t, createdConfig(t, m))
	assert.Equal(t, "admin", username, "the image is told the username")
	assert.Equal(t, "s3cret", password, "and the password")
	assert.Equal(t, "admin", c.Username(), "the container reports the username")
	assert.Equal(t, "s3cret", c.Password(), "and the password")

	uri := parseURI(t, c)
	uriPassword, _ := uri.User.Password()
	assert.Equal(t, "admin", uri.User.Username(), "the URI authenticates on its own")
	assert.Equal(t, "s3cret", uriPassword, "with the same password")
}

func TestCredentialsSurviveTheURIWhateverTheyContain(t *testing.T) {
	const username, password = "us er@corp", "p@ss:w/rd?#[]$+,%"
	m, port := authMock(t)

	c, err := mongod.Start(context.Background(), mongod.WithDocker(m).WithPort(port).WithAuth(username, password))
	require.NoError(t, err, "any username and password are accepted")
	t.Cleanup(func() { _ = c.Stop(context.Background()) })

	uri := parseURI(t, c)
	uriPassword, _ := uri.User.Password()
	assert.Equal(t, username, uri.User.Username(), "the username decodes to what was given")
	assert.Equal(t, password, uriPassword, "and so does the password")
	assert.NotContains(t, c.URI(), "+", "some drivers read a bare + as a space, so it is always encoded")
	assert.NotContains(t, c.URI(), "$", "the connection string format reserves $")

	_, sent := rootUser(t, createdConfig(t, m))
	assert.Equal(t, password, sent, "the image is given the password itself, not its encoding")
}

func TestPublishingBeyondLoopbackGeneratesCredentials(t *testing.T) {
	first := createdFor(t, mongod.WithHostIP(siblingGateway))
	second := createdFor(t, mongod.WithHostIP(siblingGateway))

	username, password := rootUser(t, first)
	_, otherPassword := rootUser(t, second)
	assert.Equal(t, mongod.DefaultUsername, username, "a mongod other machines can reach is never created without a root user")
	assert.NotEmpty(t, password, "with a password")
	assert.NotEqual(t, password, otherPassword, "that is different for every container")
}

func TestWithAuthIsKeptWhenPublishingBeyondLoopback(t *testing.T) {
	username, password := rootUser(t, createdFor(t, mongod.WithHostIP(siblingGateway).WithAuth("admin", "s3cret")))

	assert.Equal(t, "admin", username, "credentials are generated only when the caller chose none")
	assert.Equal(t, "s3cret", password, "so the caller's password stands")
}

func TestWithoutAuthLeavesAWidePublishOpen(t *testing.T) {
	username, _ := rootUser(t, createdFor(t, mongod.WithHostIP(siblingGateway).WithoutAuth()))

	assert.Empty(t, username, "WithoutAuth says the network is trusted, so nothing is generated")
}

func TestAuthMergesAsOneDecision(t *testing.T) {
	cases := []struct {
		name string
		opts []*mongod.Options
		want string
	}{
		{"a later WithoutAuth removes earlier credentials",
			[]*mongod.Options{mongod.WithAuth("admin", "s3cret"), mongod.WithHostIP(siblingGateway).WithoutAuth()}, ""},
		{"a later WithAuth overrides an earlier WithoutAuth",
			[]*mongod.Options{mongod.WithoutAuth(), mongod.WithAuth("admin", "s3cret")}, "admin"},
		{"a struct literal overrides like a helper",
			[]*mongod.Options{mongod.WithAuth("admin", "s3cret"), {Username: "other", Password: "pw"}}, "other"},
		{"an unrelated later option leaves them alone",
			[]*mongod.Options{mongod.WithAuth("admin", "s3cret"), mongod.WithReplicaSet("rs0")}, "admin"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			username, _ := rootUser(t, createdFor(t, tc.opts...))

			assert.Equal(t, tc.want, username, "the later Options decides, and only if it says anything about credentials")
		})
	}
}

func TestHalfAPairOfCredentialsIsRefused(t *testing.T) {
	cases := map[string]*mongod.Options{
		"a username with no password": mongod.WithAuth("admin", ""),
		"a password with no username": mongod.WithAuth("", "s3cret"),
		"a literal with only a name":  {Username: "admin"},
	}
	for name, opts := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := opts.Resolve()

			assert.ErrorIs(t, err, mongod.ErrIncompleteCredentials, "an image creates no user from half a pair, so mongod would come up open")
			assert.ErrorIs(t, err, dockerclient.ErrInvalidArgument, "it is a mistake in the options")
		})
	}
}

func TestNoAuthWithCredentialsIsRefused(t *testing.T) {
	_, err := (&mongod.Options{Username: "admin", Password: "s3cret", NoAuth: true}).Resolve()

	assert.ErrorIs(t, err, mongod.ErrConflictingAuth, "guessing which was meant would either expose a database or lock a test out")
}

// keyFile is the file a replica set container is given before it starts.
type keyFile struct {
	path    string
	header  tar.Header
	content string
}

// startRecordingKeyFile starts a container and returns any key file copied
// into it. imageUser is the user the image declares.
func startRecordingKeyFile(t *testing.T, opts *mongod.Options, imageUser string) (*dockermock.Mock, []keyFile) {
	t.Helper()
	m, port := authMock(t)
	m.ImageInspectFunc = func(context.Context, string) (dockerclient.ImageInspect, error) {
		return dockerclient.ImageInspect{Config: dockerclient.ImageConfig{User: imageUser}}, nil
	}
	var files []keyFile
	m.CopyArchiveToContainerFunc = func(_ context.Context, _, destDir string, archive io.Reader) error {
		tr := tar.NewReader(archive)
		for {
			header, err := tr.Next()
			if errors.Is(err, io.EOF) {
				return nil
			}
			require.NoError(t, err, "the archive must be one the daemon can read")
			content, err := io.ReadAll(tr)
			require.NoError(t, err, "the entry must be readable")
			files = append(files, keyFile{path: destDir + "/" + header.Name, header: *header, content: string(content)})
		}
	}
	c, err := mongod.Start(context.Background(), opts.WithDocker(m).WithPort(port))
	require.NoError(t, err, "the start must succeed")
	t.Cleanup(func() { _ = c.Stop(context.Background()) })
	return m, files
}

func TestReplicaSetWithAuthIsGivenAKeyFile(t *testing.T) {
	cases := []struct {
		name      string
		imageUser string
		owner     int
	}{
		{"the official image declares no user and runs mongod as 999", "", 999},
		{"MongoDB's builds declare a named user, which is 1000", "mongod", 1000},
		{"a numeric user is used as given", "1001:0", 1001},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m, files := startRecordingKeyFile(t, mongod.WithReplicaSet("rs0").WithAuth("admin", "s3cret"), tc.imageUser)

			require.Len(t, files, 1, "a replica set member that authenticates will not start without a key file")
			file := files[0]
			path, passed := flagValue(createdConfig(t, m).Cmd, "--keyFile")
			require.True(t, passed, "mongod is pointed at the key")
			assert.Equal(t, file.path, path, "at the path it was copied to")
			assert.Zero(t, file.header.Mode&0o077, "mongod refuses a key that group or other can read")
			assert.Equal(t, tc.owner, file.header.Uid, "so the key has to belong to the user mongod runs as")
			assert.NotEmpty(t, file.content, "the key is not empty")

			methods := m.Methods()
			assert.Less(t, slices.Index(methods, "CopyArchiveToContainer"), slices.Index(methods, "ContainerStart"),
				"the key has to be in place before mongod runs")
		})
	}
}

func TestNoKeyFileUnlessBothAReplicaSetAndAuth(t *testing.T) {
	cases := map[string]*mongod.Options{
		"a replica set without authentication":  mongod.WithReplicaSet("rs0"),
		"authentication without a replica set":  mongod.WithAuth("admin", "s3cret"),
		"Atlas Local, which brings its own key": mongod.WithMongoImage(mongod.ImageAtlasLocal).WithAuth("admin", "s3cret"),
	}
	for name, opts := range cases {
		t.Run(name, func(t *testing.T) {
			m, files := startRecordingKeyFile(t, opts, "")

			assert.Empty(t, files, "nothing is copied into a container that does not need it")
			_, passed := flagValue(createdConfig(t, m).Cmd, "--keyFile")
			assert.False(t, passed, "--keyFile turns authentication on, so it is not passed unasked")
		})
	}
}

func TestAFailedKeyFileCopyRemovesTheContainer(t *testing.T) {
	m, port := readyMock(t)
	refused := errors.New("the daemon refused the archive")
	m.CopyArchiveToContainerFunc = func(context.Context, string, string, io.Reader) error { return refused }

	_, err := mongod.Start(context.Background(), mongod.WithDocker(m).WithPort(port).WithReplicaSet("rs0").WithAuth("admin", "s3cret"))

	require.ErrorIs(t, err, refused, "the daemon's own error explains the failure")
	var startErr *mongod.StartError
	require.ErrorAs(t, err, &startErr, "and it is reported as a step of the start")
	assert.Equal(t, mongod.OpCopyKeyFile, startErr.Op, "naming the step")
	assert.Empty(t, m.CallsTo("ContainerStart"), "a mongod that would exit for want of its key is not started")
	assert.Len(t, m.CallsTo("ContainerRemove"), 1, "and the container is removed")
}

// everythingLogger keeps every word it is given, keys and values included.
type everythingLogger struct {
	mu    sync.Mutex
	lines []string
}

func (l *everythingLogger) log(msg string, keysAndValues ...any) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.lines = append(l.lines, msg+" "+fmt.Sprint(keysAndValues...))
}

func (l *everythingLogger) Debug(msg string, kv ...any) { l.log(msg, kv...) }
func (l *everythingLogger) Info(msg string, kv ...any)  { l.log(msg, kv...) }
func (l *everythingLogger) Warn(msg string, kv ...any)  { l.log(msg, kv...) }
func (l *everythingLogger) Error(msg string, kv ...any) { l.log(msg, kv...) }

func (l *everythingLogger) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return strings.Join(l.lines, "\n")
}

func TestThePasswordIsNeverLogged(t *testing.T) {
	m, port := authMock(t)
	logger := &everythingLogger{}

	c, err := mongod.Start(context.Background(), mongod.WithDocker(m).WithPort(port).WithLogger(logger).WithAuth("admin", "s3cret-do-not-log"))
	require.NoError(t, err, "the start must succeed")
	require.NoError(t, c.Stop(context.Background()), "and so must the stop")

	assert.NotContains(t, logger.String(), "s3cret-do-not-log", "logs are shared more widely than access to the daemon")
}

func TestPrintingAContainerDoesNotPrintThePassword(t *testing.T) {
	m, port := authMock(t)
	c, err := mongod.Start(context.Background(), mongod.WithDocker(m).WithPort(port).WithAuth("admin", "s3cret-do-not-print"))
	require.NoError(t, err, "the start must succeed")
	t.Cleanup(func() { _ = c.Stop(context.Background()) })

	for _, verb := range []string{"%v", "%+v", "%#v", "%s"} {
		printed := fmt.Sprintf(verb, c)

		assert.NotContains(t, printed, "s3cret-do-not-print", "a container is printed by test logs and failure dumps, which CI keeps (%s)", verb)
		assert.Contains(t, printed, c.Name(), "and what is printed still says which container it is (%s)", verb)
	}
}

// MongoDB's community image builds its create-user script by pasting the
// credentials into a JavaScript template literal, so these characters either
// change the password or create no user at all.
func TestCredentialsAnImageCannotCreateAreRefused(t *testing.T) {
	cases := map[string]*mongod.Options{
		"a backslash in the password": mongod.WithAuth("admin", `pa\ss`),
		"a backtick in the password":  mongod.WithAuth("admin", "pa`ss"),
		"${ in the password":          mongod.WithAuth("admin", "a${1+1}b"),
		"a backtick in the username":  mongod.WithAuth("ad`min", "s3cret"),
	}
	for name, opts := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := opts.Resolve()

			assert.ErrorIs(t, err, mongod.ErrUnusableCredentials, "a start must not succeed with credentials that will never authenticate")
			assert.ErrorIs(t, err, dockerclient.ErrInvalidArgument, "it is a mistake in the options")
		})
	}

	_, err := mongod.WithAuth("admin", `p@ss:w/rd?#[]$+,%{}`).Resolve()
	assert.NoError(t, err, "every other character is accepted, a lone $ and braces included")
}
