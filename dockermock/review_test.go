package dockermock_test

import (
	"bytes"
	"context"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tophergopher/mongotest/dockerapi"
	"github.com/tophergopher/mongotest/dockerclient"
	"github.com/tophergopher/mongotest/dockermock"
)

const testDigest = "sha256:1111111111111111111111111111111111111111111111111111111111111111"

func TestFakeContainersDoesNotAliasLiveState(t *testing.T) {
	// ContainerState carries maps, so copying the struct copies the map
	// headers and every returned element still points at state the Fake goes
	// on mutating. Containers() exists for a test to assert cleanup, so it is
	// called concurrently with the client under test by design.
	ctx := context.Background()
	f := dockermock.NewFake(dockermock.WithImages("mongo:8"))
	id, _, err := f.ContainerCreate(ctx, "mongotest-race", dockerclient.ContainerConfig{
		Image:        "mongo:8",
		ExposedPorts: map[string]struct{}{"27017/tcp": {}},
		HostConfig: &dockerclient.HostConfig{PortBindings: map[string][]dockerclient.PortBinding{
			"27017/tcp": {{HostIP: "127.0.0.1"}},
		}},
	})
	require.NoError(t, err, "creating the container the test races against")

	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		for i := 0; i < 200; i++ {
			for _, st := range f.Containers() {
				for k := range st.Files {
					_ = k
				}
				for k := range st.Ports {
					_ = k
				}
			}
		}
	}()
	go func() {
		defer wg.Done()
		for i := 0; i < 200; i++ {
			_ = f.CopyToContainer(ctx, id, "/etc", []dockerclient.File{
				{Name: "server.pem", Mode: 0o600, Content: []byte("x")},
			})
		}
	}()
	wg.Wait()
}

func TestFakeRejectsATraversingDestination(t *testing.T) {
	// The real clients reject "..", the Fake did not, so a caller could get a
	// green test here and ErrInvalidArgument against a daemon.
	ctx := context.Background()
	f := dockermock.NewFake(dockermock.WithImages("mongo:8"))
	id, _, err := f.ContainerCreate(ctx, "", dockerclient.ContainerConfig{Image: "mongo:8"})
	require.NoError(t, err, "creating a container to copy into")
	files := []dockerclient.File{{Name: "server.pem", Mode: 0o600, Content: []byte("x")}}

	for _, dest := range []string{"", "relative", "/etc/../root", "/etc/.."} {
		err := f.CopyToContainer(ctx, id, dest, files)
		assert.ErrorIs(t, err, dockerclient.ErrInvalidArgument,
			"CopyToContainer must refuse the destination %q, as the real clients do", dest)

		err = f.CopyArchiveToContainer(ctx, id, dest, strings.NewReader(""))
		assert.ErrorIs(t, err, dockerclient.ErrInvalidArgument,
			"CopyArchiveToContainer must refuse the destination %q too", dest)
	}
}

func TestFakeGuardsExecIDs(t *testing.T) {
	// Both real clients check the id first, so a malformed one is
	// ErrInvalidArgument. Returning ErrNotFound instead sends a caller down
	// the "pull and retry" branch for what is a bug in their own code.
	ctx := context.Background()
	f := dockermock.NewFake()
	for _, bad := range []string{"", "a/b", "../x", "a b"} {
		err := f.ExecStartTo(ctx, bad, nil, nil)
		assert.ErrorIs(t, err, dockerclient.ErrInvalidArgument,
			"ExecStartTo must reject the exec id %q before looking it up", bad)

		_, err = f.ExecInspect(ctx, bad)
		assert.ErrorIs(t, err, dockerclient.ErrInvalidArgument,
			"ExecInspect must reject the exec id %q before looking it up", bad)
	}
}

func TestFakeReportsNoTagForADigestPull(t *testing.T) {
	// ParseImageRef leaves Tag empty for a digest reference, so joining with
	// a colon produced "mongo:". A real daemon reports no tag at all for an
	// image pulled by digest.
	ctx := context.Background()
	f := dockermock.NewFake()
	ref := "mongo@" + testDigest
	require.NoError(t, f.ImagePull(ctx, ref), "pulling by digest must succeed")

	img, err := f.ImageInspect(ctx, ref)
	require.NoError(t, err, "inspecting the digest-pulled image")
	for _, tag := range img.RepoTags {
		assert.NotEmpty(t, strings.TrimSuffix(tag, ":"), "a digest pull must not produce a bare %q tag", tag)
		assert.False(t, strings.HasSuffix(tag, ":"), "RepoTags must not end in a colon, got %q", tag)
	}
}

func TestServeFakePreservesADigestReference(t *testing.T) {
	// ImagePull sends a digest in the tag query parameter, following the
	// official client. Reassembling with a colon yields "mongo:sha256:...",
	// which then fails to parse, so the digest path was untestable through
	// the mock and ServeFake stopped mirroring Fake.
	daemon := dockermock.NewDaemon(dockermock.OverTCP())
	t.Cleanup(daemon.Close)
	fake := dockermock.NewFake()
	daemon.ServeFake(fake)

	docker, err := dockerapi.New(dockerapi.WithHost(daemon.Host()))
	require.NoError(t, err, "building a client against the fake daemon")

	ref := "mongo@" + testDigest
	require.NoError(t, docker.ImagePull(context.Background(), ref),
		"a digest pull must survive the round trip through ServeFake")

	_, err = docker.ImageInspect(context.Background(), ref)
	require.NoError(t, err, "the image must be inspectable under the reference it was pulled as")
}

func TestServeFakeStillHandlesATagReference(t *testing.T) {
	daemon := dockermock.NewDaemon(dockermock.OverTCP())
	t.Cleanup(daemon.Close)
	daemon.ServeFake(dockermock.NewFake())

	docker, err := dockerapi.New(dockerapi.WithHost(daemon.Host()))
	require.NoError(t, err, "building a client")
	require.NoError(t, docker.ImagePull(context.Background(), "mongo:8"), "an ordinary tag pull must still work")

	img, err := docker.ImageInspect(context.Background(), "mongo:8")
	require.NoError(t, err, "the tagged image must be inspectable")
	assert.Contains(t, img.RepoTags, "mongo:8", "a tag pull must report the tag it was pulled as")
}

func TestFakeAndRealClientAgreeOnDestinations(t *testing.T) {
	// The point of the doubles is that code tested against them behaves the
	// same against a daemon. Drive both through the same table.
	daemon := dockermock.NewDaemon(dockermock.OverTCP())
	t.Cleanup(daemon.Close)
	fake := dockermock.NewFake(dockermock.WithImages("mongo:8"))
	daemon.ServeFake(fake)
	real_, err := dockerapi.New(dockerapi.WithHost(daemon.Host()))
	require.NoError(t, err, "building a real client against the fake daemon")

	ctx := context.Background()
	id, _, err := real_.ContainerCreate(ctx, "", dockerclient.ContainerConfig{Image: "mongo:8"})
	require.NoError(t, err, "creating a container through the daemon")
	files := []dockerclient.File{{Name: "a.pem", Mode: 0o600, Content: []byte("x")}}

	for _, dest := range []string{"", "relative", "/etc/../root"} {
		fakeErr := fake.CopyToContainer(ctx, id, dest, files)
		realErr := real_.CopyToContainer(ctx, id, dest, files)
		assert.Equal(t, fakeErr == nil, realErr == nil,
			"the Fake and the real client must agree on whether %q is a valid destination (fake=%v real=%v)", dest, fakeErr, realErr)
	}

	var buf bytes.Buffer
	assert.Zero(t, buf.Len(), "no output is expected; the buffer exists only to keep the imports honest")
}
