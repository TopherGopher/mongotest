package mongod_test

import (
	"archive/tar"
	"context"
	"errors"
	"fmt"
	"io"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/tophergopher/mongotest/dockerclient"
	"github.com/tophergopher/mongotest/dockermock"
	"github.com/tophergopher/mongotest/mongod"
)

func TestALocalContainerHasNoCredentials(t *testing.T) {
	m, _ := readyMock(t)

	c := mustStart(t, m)

	username, _ := rootUser(t, created(t, m))
	assert.Empty(t, username, "nothing off this machine can reach a loopback port, so no user is created")
	assert.Empty(t, c.Username(), "the container reports no user")
	assert.Nil(t, parseURI(t, c).User, "and the URI carries none")
}

func TestWithAuthCreatesTheRootUserAndPutsItInTheURI(t *testing.T) {
	m, _ := authMock(t)

	c := mustStart(t, m, mongod.WithAuth("admin", "s3cret"))

	username, password := rootUser(t, created(t, m))
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
	m, _ := authMock(t)

	c := mustStart(t, m, mongod.WithAuth(username, password))

	uri := parseURI(t, c)
	uriPassword, _ := uri.User.Password()
	assert.Equal(t, username, uri.User.Username(), "the username decodes to what was given")
	assert.Equal(t, password, uriPassword, "and so does the password")
	assert.NotContains(t, c.URI(), "+", "some drivers read a bare + as a space, so it is always encoded")
	assert.NotContains(t, c.URI(), "$", "the connection string format reserves $")

	_, sent := rootUser(t, created(t, m))
	assert.Equal(t, password, sent, "the image is given the password itself, not its encoding")
}

func TestAContainerOtherMachinesCanReachGetsGeneratedCredentials(t *testing.T) {
	first := requestFor(t, mongod.WithHostIP(siblingGateway))
	second := requestFor(t, mongod.WithHostIP(siblingGateway))

	username, password := rootUser(t, first)
	_, otherPassword := rootUser(t, second)
	assert.Equal(t, "0.0.0.0", first.HostConfig.PortBindings["27017/tcp"][0].HostIP, "an address that is not loopback needs the port on every interface")
	assert.Equal(t, mongod.DefaultUsername, username, "so mongod is never created without a root user")
	assert.NotEmpty(t, password, "with a password")
	assert.NotEqual(t, password, otherPassword, "that is different for every container")
}

func TestCredentialOptionsOnAContainerOtherMachinesCanReach(t *testing.T) {
	cases := []struct {
		name string
		opts []mongod.Option
		want string
	}{
		{"WithAuth is kept", []mongod.Option{mongod.WithAuth("admin", "s3cret")}, "admin"},
		{"WithoutAuth leaves it open", []mongod.Option{mongod.WithoutAuth()}, ""},
		{"a later WithoutAuth removes earlier credentials", []mongod.Option{mongod.WithAuth("admin", "s3cret"), mongod.WithoutAuth()}, ""},
		{"a later WithAuth overrides an earlier WithoutAuth", []mongod.Option{mongod.WithoutAuth(), mongod.WithAuth("admin", "s3cret")}, "admin"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			opts := append([]mongod.Option{mongod.WithHostIP(siblingGateway)}, tc.opts...)

			username, _ := rootUser(t, requestFor(t, opts...))

			assert.Equal(t, tc.want, username, "the caller's choice stands, and nothing is generated over it")
		})
	}
}

func TestCredentialsThatCannotWorkAreRefused(t *testing.T) {
	cases := []struct {
		name string
		opt  mongod.Option
		want error
	}{
		{"a username with no password", mongod.WithAuth("admin", ""), mongod.ErrIncompleteCredentials},
		{"a password with no username", mongod.WithAuth("", "s3cret"), mongod.ErrIncompleteCredentials},
		// MongoDB's community image pastes credentials into a script, where
		// these change the password or create no user at all.
		{"a backslash", mongod.WithAuth("admin", `pa\ss`), mongod.ErrUnusableCredentials},
		{"a backtick", mongod.WithAuth("ad`min", "s3cret"), mongod.ErrUnusableCredentials},
		{"${", mongod.WithAuth("admin", "a${1+1}b"), mongod.ErrUnusableCredentials},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m, _ := authMock(t)

			_, err := start(t, m, tc.opt)

			require.ErrorIs(t, err, tc.want, "a start must not succeed with credentials that will never authenticate")
			assert.ErrorIs(t, err, dockerclient.ErrInvalidArgument, "it is a mistake in the options")
			assert.Empty(t, m.CallsTo("ContainerCreate"), "caught before anything is created")
		})
	}
}

// keyFile is the file a replica set container is given before it starts.
type keyFile struct {
	path    string
	header  tar.Header
	content string
}

// startRecordingKeyFile starts a container and returns any key file copied
// into it. imageUser is the user the image declares.
func startRecordingKeyFile(t *testing.T, imageUser string, opts ...mongod.Option) (*dockermock.Mock, []keyFile) {
	t.Helper()
	m, _ := authMock(t)
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
	mustStart(t, m, opts...)
	return m, files
}

func TestAReplicaSetWithCredentialsIsGivenAKeyFile(t *testing.T) {
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
			m, files := startRecordingKeyFile(t, tc.imageUser, mongod.WithReplicaSet("rs0"), mongod.WithAuth("admin", "s3cret"))

			require.Len(t, files, 1, "a replica set member that authenticates will not start without a key file")
			file := files[0]
			path, passed := flagValue(created(t, m).Cmd, "--keyFile")
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

func TestNoKeyFileUnlessBothAReplicaSetAndCredentials(t *testing.T) {
	cases := map[string][]mongod.Option{
		"a replica set without credentials":     {mongod.WithReplicaSet("rs0")},
		"credentials without a replica set":     {mongod.WithAuth("admin", "s3cret")},
		"Atlas Local, which brings its own key": {mongod.WithMongoImage(mongod.ImageAtlasLocal), mongod.WithAuth("admin", "s3cret")},
	}
	for name, opts := range cases {
		t.Run(name, func(t *testing.T) {
			m, files := startRecordingKeyFile(t, "", opts...)

			assert.Empty(t, files, "nothing is copied into a container that does not need it")
			_, passed := flagValue(created(t, m).Cmd, "--keyFile")
			assert.False(t, passed, "--keyFile turns authentication on, so it is not passed unasked")
		})
	}
}

func TestAFailedKeyFileCopyRemovesTheContainer(t *testing.T) {
	m, _ := authMock(t)
	refused := errors.New("the daemon refused the archive")
	m.CopyArchiveToContainerFunc = func(context.Context, string, string, io.Reader) error { return refused }

	_, err := start(t, m, mongod.WithReplicaSet("rs0"), mongod.WithAuth("admin", "s3cret"))

	var startErr *mongod.StartError
	require.ErrorAs(t, err, &startErr, "the failure is reported as a step of the start")
	assert.Equal(t, mongod.OpCopyKeyFile, startErr.Op, "naming the step")
	assert.ErrorIs(t, err, refused, "and wrapping the daemon's error")
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

func TestThePasswordIsNeverLoggedOrPrinted(t *testing.T) {
	const password = "s3cret-keep-out"
	m, _ := authMock(t)
	logger := &everythingLogger{}

	c := mustStart(t, m, mongod.WithLogger(logger), mongod.WithAuth("admin", password))
	printed := fmt.Sprintf("%v %+v %#v %s", c, c, c, c)
	require.NoError(t, c.Stop(context.Background()), "the stop must succeed")

	assert.NotEmpty(t, logger.String(), "the start is logged")
	assert.NotContains(t, logger.String(), password, "without the password: logs are shared more widely than the daemon")
	assert.NotContains(t, printed, password, "and printing a container, as a failing test does, hides it too")
	assert.Contains(t, printed, c.Name(), "while still saying which container it is")
}
