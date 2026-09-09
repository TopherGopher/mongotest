package dockermock_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/tophergopher/mongotest/dockerapi"
	"github.com/tophergopher/mongotest/dockerclient"
	"github.com/tophergopher/mongotest/dockermock"
)

// The Fake's interface methods are measured by BenchmarkFake, which runs the
// shared suite. What is measured here is the rest of the package: the Mock's
// recording, the Daemon's routing, and construction.

func BenchmarkMock(b *testing.B) {
	b.Run("call with no function set", func(b *testing.B) {
		m := &dockermock.Mock{}
		ctx := context.Background()
		b.ReportAllocs()
		for b.Loop() {
			if err := m.ContainerStart(ctx, "c1"); err != nil {
				b.Fatal(err)
			}
			m.Reset() // otherwise the recording grows without bound
		}
	})
	b.Run("call with a function set", func(b *testing.B) {
		m := &dockermock.Mock{
			ContainerCreateFunc: func(ctx context.Context, name string, cfg dockerclient.ContainerConfig) (string, []string, error) {
				return "c1", nil, nil
			},
		}
		ctx := context.Background()
		cfg := dockerclient.ContainerConfig{Image: "mongo:8"}
		b.ReportAllocs()
		for b.Loop() {
			if _, _, err := m.ContainerCreate(ctx, "", cfg); err != nil {
				b.Fatal(err)
			}
			m.Reset()
		}
	})
	b.Run("Calls", func(b *testing.B) {
		m := &dockermock.Mock{}
		ctx := context.Background()
		for range 100 {
			_ = m.ContainerStart(ctx, "c1")
		}
		b.ReportAllocs()
		for b.Loop() {
			if len(m.Calls()) != 100 {
				b.Fatal("unexpected call count")
			}
		}
	})
	b.Run("CallsTo", func(b *testing.B) {
		m := &dockermock.Mock{}
		ctx := context.Background()
		for range 100 {
			_ = m.ContainerStart(ctx, "c1")
			_ = m.ImagePull(ctx, "mongo:8")
		}
		b.ReportAllocs()
		for b.Loop() {
			if len(m.CallsTo("ImagePull")) != 100 {
				b.Fatal("unexpected call count")
			}
		}
	})
	b.Run("Methods", func(b *testing.B) {
		m := &dockermock.Mock{}
		ctx := context.Background()
		for range 100 {
			_ = m.ContainerStart(ctx, "c1")
		}
		b.ReportAllocs()
		for b.Loop() {
			_ = m.Methods()
		}
	})
}

func BenchmarkMockHost(b *testing.B) {
	m := &dockermock.Mock{}
	b.ReportAllocs()
	for b.Loop() {
		if m.Host() == "" {
			b.Fatal("a Mock must still report an address")
		}
	}
}

func BenchmarkMockReset(b *testing.B) {
	m := &dockermock.Mock{}
	ctx := context.Background()
	b.ReportAllocs()
	for b.Loop() {
		_ = m.ContainerStart(ctx, "c1")
		m.Reset()
	}
}

// Hijack is the expensive helper: it takes the connection over and writes the
// framed stream by hand, which is the path every exec in a test goes through.
func BenchmarkHijack(b *testing.B) {
	d := dockermock.NewDaemon(dockermock.OverTCP())
	b.Cleanup(d.Close)
	d.ServeDefaults()
	stream := append(dockermock.Frame(dockermock.StreamStdout, "1\n"),
		dockermock.Frame(dockermock.StreamStderr, "")...)
	d.Handle(http.MethodPost, "/exec/{id}/start", func(w http.ResponseWriter, r *http.Request) {
		dockermock.Hijack(w, stream)
	})

	docker, err := dockerapi.New(dockerapi.WithHost(d.Host()))
	if err != nil {
		b.Fatalf("the client could not be built against the daemon: %v", err)
	}
	ctx := context.Background()
	b.ReportAllocs()
	for b.Loop() {
		if _, err := docker.Exec(ctx, dockermock.DefaultContainerID, "mongosh", "--eval", "1"); err != nil {
			b.Fatalf("the hijacked exec failed: %v", err)
		}
	}
}

func BenchmarkNewFake(b *testing.B) {
	b.ReportAllocs()
	for b.Loop() {
		_ = dockermock.NewFake(dockermock.WithImages("mongo:8"))
	}
}

func BenchmarkFakeContainers(b *testing.B) {
	f := dockermock.NewFake(dockermock.WithImages("mongo:8"))
	ctx := context.Background()
	for range 20 {
		if _, _, err := f.ContainerCreate(ctx, "", dockerclient.ContainerConfig{Image: "mongo:8"}); err != nil {
			b.Fatal(err)
		}
	}
	b.ReportAllocs()
	for b.Loop() {
		if len(f.Containers()) != 20 {
			b.Fatal("unexpected container count")
		}
	}
}

func BenchmarkNewDaemon(b *testing.B) {
	b.Run("unix", func(b *testing.B) {
		b.ReportAllocs()
		for b.Loop() {
			d := dockermock.NewDaemon()
			d.Close()
		}
	})
	b.Run("tcp", func(b *testing.B) {
		b.ReportAllocs()
		for b.Loop() {
			d := dockermock.NewDaemon(dockermock.OverTCP())
			d.Close()
		}
	})
}

// benchDaemonClient is a real client pointed at a Daemon, which is what the
// Daemon costs a test that uses it.
func benchDaemonClient(b *testing.B, serve func(*dockermock.Daemon)) dockerclient.Client {
	b.Helper()
	d := dockermock.NewDaemon()
	b.Cleanup(d.Close)
	serve(d)
	c, err := dockerapi.New(dockerapi.WithHost(d.Host()))
	if err != nil {
		b.Fatalf("a client against the fake daemon must construct: %v", err)
	}
	if err := c.Negotiate(context.Background()); err != nil {
		b.Fatalf("negotiating against the fake daemon: %v", err)
	}
	return c
}

func BenchmarkDaemonServeDefaults(b *testing.B) {
	c := benchDaemonClient(b, func(d *dockermock.Daemon) { d.ServeDefaults() })
	ctx := context.Background()
	b.ReportAllocs()
	for b.Loop() {
		if _, err := c.ContainerInspect(ctx, dockermock.DefaultContainerID); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkDaemonServeFake(b *testing.B) {
	// The stateful routing, which is what the conformance suite runs on.
	c := benchDaemonClient(b, func(d *dockermock.Daemon) { d.ServeFake(dockermock.NewFake(dockermock.WithImages("mongo:8"))) })
	ctx := context.Background()
	id, _, err := c.ContainerCreate(ctx, "", dockerclient.ContainerConfig{Image: "mongo:8"})
	if err != nil {
		b.Fatal(err)
	}
	b.ReportAllocs()
	for b.Loop() {
		if _, err := c.ContainerInspect(ctx, id); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkDaemonRequests(b *testing.B) {
	d := dockermock.NewDaemon()
	b.Cleanup(d.Close)
	d.ServeDefaults()
	c, err := dockerapi.New(dockerapi.WithHost(d.Host()))
	if err != nil {
		b.Fatal(err)
	}
	ctx := context.Background()
	for range 50 {
		_ = c.ContainerStart(ctx, "abc")
	}
	b.ReportAllocs()
	for b.Loop() {
		if len(d.Requests()) == 0 {
			b.Fatal("expected recorded requests")
		}
	}
}

func BenchmarkDaemonServeVersion(b *testing.B) {
	d := dockermock.NewDaemon()
	b.Cleanup(d.Close)
	b.ReportAllocs()
	for b.Loop() {
		d.ServeVersion("1.44", "1.24")
	}
}

func BenchmarkDaemonHandle(b *testing.B) {
	d := dockermock.NewDaemon()
	b.Cleanup(d.Close)
	h := func(w http.ResponseWriter, r *http.Request) { dockermock.JSON(w, 200, map[string]string{"ok": "1"}) }
	b.ReportAllocs()
	for b.Loop() {
		d.Handle("GET", "/containers/{id}/json", h)
	}
}

func BenchmarkFrame(b *testing.B) {
	b.ReportAllocs()
	for b.Loop() {
		_ = dockermock.Frame(dockermock.StreamStdout, "mongod output line\n")
	}
}

func BenchmarkDaemonHost(b *testing.B) {
	d := dockermock.NewDaemon()
	b.Cleanup(d.Close)
	b.ReportAllocs()
	for b.Loop() {
		if d.Host() == "" || d.URL() == "" {
			b.Fatal("a started daemon must report an address")
		}
	}
}

func BenchmarkDaemonReset(b *testing.B) {
	d := dockermock.NewDaemon()
	b.Cleanup(d.Close)
	b.ReportAllocs()
	for b.Loop() {
		d.Reset()
	}
}

// The response helpers run on every request a custom route serves, so their
// cost is paid once per call in every test that uses a Daemon.

func BenchmarkJSON(b *testing.B) {
	inspect := dockerclient.ContainerInspect{
		ID:    dockermock.DefaultContainerID,
		Name:  dockermock.DefaultContainerName,
		State: dockerclient.ContainerState{Status: "running", Running: true},
		Config: dockerclient.InspectedConfig{
			Image:  "mongo:8",
			Labels: map[string]string{"mongotest": "benchmark"},
		},
		NetworkSettings: dockerclient.NetworkSettings{Ports: map[string][]dockerclient.PortBinding{
			"27017/tcp": {{HostIP: "127.0.0.1", HostPort: dockermock.DefaultHostPort}},
		}},
	}
	b.ReportAllocs()
	for b.Loop() {
		w := httptest.NewRecorder()
		dockermock.JSON(w, http.StatusOK, inspect)
	}
}

func BenchmarkError(b *testing.B) {
	b.ReportAllocs()
	for b.Loop() {
		w := httptest.NewRecorder()
		dockermock.Error(w, http.StatusNotFound, "No such container: c0ffee1234ab")
	}
}

func BenchmarkPathParam(b *testing.B) {
	// The Daemon's own router is what installs the captured values, so the
	// benchmark goes through a real request rather than a hand-built one.
	d := dockermock.NewDaemon(dockermock.OverTCP())
	b.Cleanup(d.Close)
	var got string
	d.Handle(http.MethodGet, "/containers/{id}/json", func(w http.ResponseWriter, r *http.Request) {
		got = dockermock.PathParam(r, "id")
		w.WriteHeader(http.StatusOK)
	})
	url := d.URL() + "/v" + dockermock.DefaultAPIVersion + "/containers/" + dockermock.DefaultContainerID + "/json"

	b.ReportAllocs()
	for b.Loop() {
		resp, err := http.Get(url)
		if err != nil {
			b.Fatalf("the daemon did not answer: %v", err)
		}
		_ = resp.Body.Close()
	}
	if got != dockermock.DefaultContainerID {
		b.Fatalf("the route did not capture the id: got %q", got)
	}
}

func BenchmarkWithImages(b *testing.B) {
	b.ReportAllocs()
	for b.Loop() {
		_ = dockermock.NewFake(dockermock.WithImages("mongo:8", "mongo:7", "mongo:6"))
	}
}

func BenchmarkFakeHost(b *testing.B) {
	f := dockermock.NewFake()
	b.ReportAllocs()
	for b.Loop() {
		if f.Host() == "" {
			b.Fatal("a Fake must still report an address")
		}
	}
}
