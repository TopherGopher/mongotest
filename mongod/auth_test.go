package mongod_test

import (
	"archive/tar"
	"context"
	"errors"
	"fmt"
	"io"
	"regexp"
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

// siblingGateway is an address that is not this machine's loopback, which is
// what makes Start publish on every interface. The listener the readiness
// probe needs is on loopback, so the tests that use this pair it with a Mock
// whose inspect reports a port on an address the probe is told to dial through
// dialThroughLoopback.
const siblingGateway = "172.17.0.1"

// credentialEnv is what an image is told its root user is. Both spellings are
// always sent; see TestCredentialsReachEveryImageFamily.
func credentialEnv(username, password string) []string {
	return []string{
		"MONGO_INITDB_ROOT_USERNAME=" + username, "MONGO_INITDB_ROOT_PASSWORD=" + password,
		"MONGODB_INITDB_ROOT_USERNAME=" + username, "MONGODB_INITDB_ROOT_PASSWORD=" + password,
	}
}

// createdFor resolves a configuration, starts it against a Mock that never
// becomes ready, and returns the create request. The start fails on its
// timeout, which is beside the point: the request is built before anything is
// waited for, and a container that is never ready lets a test publish beyond
// loopback without needing something listening on an address this machine
// does not have.
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
	require.ErrorIs(t, err, mongod.ErrNotReady, "this double never reports a mongod, so the start is expected to end on its timeout and nothing else")
	return createdConfig(t, m)
}

func TestLoopbackStartsWithoutCredentials(t *testing.T) {
	m, port := readyMock(t)

	c, err := mongod.Start(context.Background(), mongod.WithDocker(m).WithPort(port))
	require.NoError(t, err, "a default start against a ready double succeeds")
	t.Cleanup(func() { _ = c.Stop(context.Background()) })

	assert.Empty(t, createdConfig(t, m).Env, "a port published on this machine's loopback is reachable by nothing else, so no user is created and the start stays as fast as it was")
	assert.Empty(t, c.Username(), "there is no user to report")
	assert.Empty(t, c.Password(), "there is no password to report")
	assert.Equal(t, fmt.Sprintf("mongodb://127.0.0.1:%d/?directConnection=true", port), c.URI(), "the URI of an unauthenticated mongod has no userinfo in it")
}

func TestWithAuthCreatesTheRootUserAndPutsItInTheURI(t *testing.T) {
	m, port := authMock(t)

	c, err := mongod.Start(context.Background(), mongod.WithDocker(m).WithPort(port).WithAuth("admin", "s3cret"))
	require.NoError(t, err, "credentials do not stop a start")
	t.Cleanup(func() { _ = c.Stop(context.Background()) })

	assert.Equal(t, credentialEnv("admin", "s3cret"), createdConfig(t, m).Env, "the image creates its root user from the environment it is created with, so the credentials have to be in the create request")
	assert.Equal(t, "admin", c.Username(), "the container reports the user it was given")
	assert.Equal(t, "s3cret", c.Password(), "the container reports the password it was given")
	assert.Equal(t, fmt.Sprintf("mongodb://admin:s3cret@127.0.0.1:%d/?directConnection=true", port), c.URI(), "the URI is what a caller hands to a driver, so it has to authenticate on its own")
}

func TestCredentialsAreEscapedInTheURI(t *testing.T) {
	m, port := authMock(t)

	c, err := mongod.Start(context.Background(), mongod.WithDocker(m).WithPort(port).WithAuth("us er@corp", "p@ss:w/rd?#[]$+,%"))
	require.NoError(t, err, "any username and password a caller can type are accepted")
	t.Cleanup(func() { _ = c.Stop(context.Background()) })

	assert.Equal(t,
		fmt.Sprintf("mongodb://us%%20er%%40corp:p%%40ss%%3Aw%%2Frd%%3F%%23%%5B%%5D%%24%%2B%%2C%%25@127.0.0.1:%d/?directConnection=true", port),
		c.URI(),
		"the connection string format reserves : / ? # [ ] @ and $, and some drivers read a bare + as a space, so everything outside the unreserved set is percent-encoded")
	assert.Contains(t, createdConfig(t, m).Env, "MONGO_INITDB_ROOT_PASSWORD=p@ss:w/rd?#[]$+,%", "the image is given the password itself; only the URI is an encoding of it")
}

func TestPublishingBeyondLoopbackGeneratesCredentials(t *testing.T) {
	first := createdFor(t, mongod.WithHostIP(siblingGateway))
	second := createdFor(t, mongod.WithHostIP(siblingGateway))

	require.Len(t, first.Env, 4, "a mongod published on every interface is reachable from whatever network the daemon's host is on, so it is never created without a root user")
	assert.Equal(t, "MONGO_INITDB_ROOT_USERNAME="+mongod.DefaultUsername, first.Env[0], "the generated user has a fixed, documented name")
	password, found := strings.CutPrefix(first.Env[1], "MONGO_INITDB_ROOT_PASSWORD=")
	require.True(t, found, "the password follows the username")
	assert.Regexp(t, regexp.MustCompile(`^[0-9a-f]{32}$`), password, "the password is 128 bits from the system's random source, in hex so that it needs no escaping anywhere")
	assert.NotEqual(t, first.Env[1], second.Env[1], "every container gets a password of its own")
	assert.Equal(t, "0.0.0.0", first.HostConfig.PortBindings["27017/tcp"][0].HostIP, "this is the publish the credentials are there to protect")
}

func TestWithAuthIsKeptWhenPublishingBeyondLoopback(t *testing.T) {
	cfg := createdFor(t, mongod.WithHostIP(siblingGateway).WithAuth("admin", "s3cret"))

	assert.Equal(t, credentialEnv("admin", "s3cret"), cfg.Env, "credentials are generated only when the caller chose none")
}

func TestWithoutAuthLeavesAWidePublishOpen(t *testing.T) {
	cfg := createdFor(t, mongod.WithHostIP(siblingGateway).WithoutAuth())

	assert.Empty(t, cfg.Env, "WithoutAuth is the caller saying the network is trusted, so nothing is generated")
}

func TestAuthMergesAsOneDecision(t *testing.T) {
	cases := []struct {
		name string
		opts []*mongod.Options
		want []string
		why  string
	}{
		{
			name: "a later WithoutAuth removes earlier credentials",
			opts: []*mongod.Options{mongod.WithAuth("admin", "s3cret"), mongod.WithHostIP(siblingGateway).WithoutAuth()},
			why:  "the three fields are one decision, so the later one replaces the earlier one whole",
		},
		{
			name: "a later WithAuth overrides an earlier WithoutAuth",
			opts: []*mongod.Options{mongod.WithoutAuth(), mongod.WithAuth("admin", "s3cret")},
			want: credentialEnv("admin", "s3cret"),
			why:  "a shared base that opts out can still be specialised by one test that wants a user",
		},
		{
			name: "a struct literal overrides like a helper does",
			opts: []*mongod.Options{mongod.WithAuth("admin", "s3cret"), {Username: "other", Password: "pw"}},
			want: credentialEnv("other", "pw"),
			why:  "a non-zero field on a literal is a decision, exactly as it is for every other setting",
		},
		{
			name: "an unrelated later option leaves them alone",
			opts: []*mongod.Options{mongod.WithAuth("admin", "s3cret"), mongod.WithReplicaSet("rs0")},
			want: credentialEnv("admin", "s3cret"),
			why:  "merging overlays only what the later Options actually set",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, createdFor(t, tc.opts...).Env, tc.why)
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

			assert.ErrorIs(t, err, mongod.ErrIncompleteCredentials, "the image creates no user from half a pair, so mongod would come up open behind a URI that claims to authenticate")
			assert.ErrorIs(t, err, dockerclient.ErrInvalidArgument, "it is a mistake in the options, which is what a caller branches on")
		})
	}
}

func TestNoAuthWithCredentialsIsRefused(t *testing.T) {
	_, err := (&mongod.Options{Username: "admin", Password: "s3cret", NoAuth: true}).Resolve()

	assert.ErrorIs(t, err, mongod.ErrConflictingAuth, "a literal can say both things at once, and guessing which was meant would either expose a database or lock a test out of one")
}

// keyFileEntry is the one file a replica set container is given before it
// starts.
type keyFileEntry struct {
	destDir string
	header  tar.Header
	content string
}

// startRecordingKeyFile starts a container against a Mock and returns the
// key file it was given, if any, along with the order of the calls.
func startRecordingKeyFile(t *testing.T, opts *mongod.Options, inspect ...func(context.Context, string) (dockerclient.ImageInspect, error)) (*dockermock.Mock, []keyFileEntry) {
	t.Helper()
	m, port := authMock(t)
	for _, f := range inspect {
		m.ImageInspectFunc = f
	}
	var entries []keyFileEntry
	m.CopyArchiveToContainerFunc = func(_ context.Context, _, destDir string, archive io.Reader) error {
		tr := tar.NewReader(archive)
		for {
			header, err := tr.Next()
			if errors.Is(err, io.EOF) {
				return nil
			}
			require.NoError(t, err, "the archive mongod builds has to be one the daemon can read")
			content, err := io.ReadAll(tr)
			require.NoError(t, err, "the entry's content has to be readable")
			entries = append(entries, keyFileEntry{destDir: destDir, header: *header, content: string(content)})
		}
	}
	c, err := mongod.Start(context.Background(), opts.WithDocker(m).WithPort(port))
	require.NoError(t, err, "the start must succeed for the key file to be worth asserting on")
	t.Cleanup(func() { _ = c.Stop(context.Background()) })
	return m, entries
}

func TestReplicaSetWithAuthIsGivenAKeyFile(t *testing.T) {
	cases := []struct {
		name      string
		image     mongod.MongoImage
		imageUser string
		owner     int
	}{
		{name: "the official image declares no user and runs mongod as 999", image: mongod.ImageDockerHub, owner: 999},
		{name: "MongoDB's community build declares mongod, which is 1000", image: mongod.ImageCommunity, imageUser: "mongod", owner: 1000},
		{name: "a mirror under another name is read the same way", image: mongod.MustParseMongoImage("registry.corp.example/platform/mongo:8"), imageUser: "mongod", owner: 1000},
		{name: "a numeric user is taken as given", image: mongod.MustParseMongoImage("registry.corp.example/hardened/mongo:8"), imageUser: "1001:0", owner: 1001},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var inspected []string
			inspect := func(_ context.Context, ref string) (dockerclient.ImageInspect, error) {
				inspected = append(inspected, ref)
				return dockerclient.ImageInspect{Config: dockerclient.ImageConfig{User: tc.imageUser}}, nil
			}
			m, entries := startRecordingKeyFile(t, mongod.WithMongoImage(tc.image).WithReplicaSet("rs0").WithAuth("admin", "s3cret"), inspect)
			assert.Equal(t, []string{tc.image.Reference()}, inspected, "the owner comes from the image that is about to run, which is what makes a mirror under another name work")

			assert.Equal(t, []string{"--replSet", "rs0", "--keyFile", "/etc/mongotest-keyfile"}, createdConfig(t, m).Cmd,
				"a replica set member that enforces authentication refuses to start without a key file, and these images do not make one")
			require.Len(t, entries, 1, "exactly one file is copied in")
			entry := entries[0]
			assert.Equal(t, "/etc", entry.destDir, "/etc exists in every image")
			assert.Equal(t, "mongotest-keyfile", entry.header.Name, "the path in the flag and the path in the archive have to be the same file")
			assert.Equal(t, int64(0o400), entry.header.Mode, "mongod refuses a key file that group or other can read")
			assert.Equal(t, tc.owner, entry.header.Uid, "and a file only its owner can read has to belong to the user mongod runs as")
			assert.Equal(t, tc.owner, entry.header.Gid, "the group follows the user")
			assert.Regexp(t, regexp.MustCompile(`^[0-9a-f]{32}$`), entry.content, "the key is random, and hex is within the base64 alphabet mongod requires of a key")

			methods := m.Methods()
			copied, started := slices.Index(methods, "CopyArchiveToContainer"), slices.Index(methods, "ContainerStart")
			assert.Less(t, copied, started, "the key has to be in place before mongod runs, which is what copying into a created container is for")
		})
	}
}

func TestNoKeyFileUnlessBothAReplicaSetAndAuth(t *testing.T) {
	cases := map[string]*mongod.Options{
		"a replica set without authentication needs no key": mongod.WithReplicaSet("rs0"),
		"authentication without a replica set needs no key": mongod.WithAuth("admin", "s3cret"),
		"Atlas Local brings a key of its own":               mongod.WithMongoImage(mongod.ImageAtlasLocal).WithAuth("admin", "s3cret"),
	}
	for name, opts := range cases {
		t.Run(name, func(t *testing.T) {
			m, entries := startRecordingKeyFile(t, opts)

			assert.Empty(t, entries, "nothing is copied into a container that does not need it")
			assert.NotContains(t, createdConfig(t, m).Cmd, "--keyFile", "--keyFile on its own turns authentication on, so it is never passed to a mongod that was not asked to authenticate")
		})
	}
}

func TestAFailedKeyFileCopyRemovesTheContainer(t *testing.T) {
	m, port := readyMock(t)
	refused := errors.New("the daemon refused the archive")
	m.CopyArchiveToContainerFunc = func(context.Context, string, string, io.Reader) error { return refused }

	_, err := mongod.Start(context.Background(), mongod.WithDocker(m).WithPort(port).WithReplicaSet("rs0").WithAuth("admin", "s3cret"))

	require.ErrorIs(t, err, refused, "the daemon's own error is what explains the failure")
	var startErr *mongod.StartError
	require.ErrorAs(t, err, &startErr, "and it is reported as a step of the start")
	assert.Contains(t, startErr.Error(), "key file", "the message names the step")
	assert.Empty(t, m.CallsTo("ContainerStart"), "a mongod that would exit for want of its key is not started")
	assert.Len(t, m.CallsTo("ContainerRemove"), 1, "the container that was created is removed again")
}

// everythingLogger keeps every word it is given, keys and values included, so
// that a test can look for something that must never be written down.
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
	require.NoError(t, err, "the start must succeed for its log to be worth reading")
	require.NoError(t, c.Stop(context.Background()), "and so must the stop")

	logged := logger.String()
	assert.NotContains(t, logged, "s3cret-do-not-log", "a log is read by more people than the daemon is, so the password stays out of it")
	assert.Contains(t, logged, "mongodb://admin:xxxxx@127.0.0.1", "the URI is still logged, with the password replaced, because where a container is listening is the most useful line a start writes")
}
