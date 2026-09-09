package mongod

import (
	"context"
	"net"
	"strconv"
	"testing"
	"time"

	"github.com/tophergopher/mongotest/dockerclient"
	"github.com/tophergopher/mongotest/dockermock"
)

// These run against the in-memory double, so what they measure is this
// package's own work: building a create request, resolving an address,
// polling for readiness and rendering a URI. The daemon round trips a real
// start also pays are measured by dockerclienttest.Benchmarks, which every
// client implementation runs.

// benchDaemon returns a stand-in daemon and a host port with something
// listening on it, which is what a start needs to be able to finish.
func benchDaemon(b *testing.B) (*dockermock.Fake, int) {
	b.Helper()
	fake := dockermock.NewFake(dockermock.WithImages(defaultImage))
	fake.Processes = func(dockermock.ContainerState) [][]string {
		return [][]string{{"1", "mongod --bind_ip_all"}}
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		b.Fatal(err)
	}
	b.Cleanup(func() { _ = listener.Close() })
	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			_ = conn.Close()
		}
	}()
	return fake, listener.Addr().(*net.TCPAddr).Port
}

// benchContainer starts one container to measure the reporting methods on.
func benchContainer(b *testing.B) *Container {
	b.Helper()
	fake, port := benchDaemon(b)
	c, err := Start(context.Background(), WithDocker(fake), WithPort(port))
	if err != nil {
		b.Fatal(err)
	}
	b.Cleanup(func() { _ = c.Stop(context.Background()) })
	return c
}

func BenchmarkStart(b *testing.B) {
	fake, port := benchDaemon(b)
	ctx := context.Background()
	b.ReportAllocs()
	for b.Loop() {
		c, err := Start(ctx, WithDocker(fake), WithPort(port))
		if err != nil {
			b.Fatal(err)
		}
		if err := c.Stop(ctx); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkStartParallel(b *testing.B) {
	// One client driven from several goroutines is what parallel tests do, so
	// this is where lock contention in the lifecycle would show up.
	fake, port := benchDaemon(b)
	ctx := context.Background()
	b.ReportAllocs()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			c, err := Start(ctx, WithDocker(fake), WithPort(port))
			if err != nil {
				b.Error(err)
				return
			}
			if err := c.Stop(ctx); err != nil {
				b.Error(err)
				return
			}
		}
	})
}

func BenchmarkContainerStop(b *testing.B) {
	fake, port := benchDaemon(b)
	ctx := context.Background()
	b.ReportAllocs()
	for b.Loop() {
		b.StopTimer()
		c, err := Start(ctx, WithDocker(fake), WithPort(port))
		if err != nil {
			b.Fatal(err)
		}
		b.StartTimer()

		if err := c.Stop(ctx); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkContainerStopAfterStopped(b *testing.B) {
	// The repeat call is the one a deferred cleanup makes after an explicit
	// stop, so it happens at least as often as the real one.
	fake, port := benchDaemon(b)
	ctx := context.Background()
	c, err := Start(ctx, WithDocker(fake), WithPort(port))
	if err != nil {
		b.Fatal(err)
	}
	if err := c.Stop(ctx); err != nil {
		b.Fatal(err)
	}
	b.ReportAllocs()
	for b.Loop() {
		if err := c.Stop(ctx); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkContainerURI(b *testing.B) {
	c := benchContainer(b)
	b.ReportAllocs()
	for b.Loop() {
		_ = c.URI()
	}
}

func BenchmarkContainerEndpoint(b *testing.B) {
	c := benchContainer(b)
	b.ReportAllocs()
	for b.Loop() {
		if _, _, err := c.Endpoint(mongoPort); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkContainerAccessors(b *testing.B) {
	c := benchContainer(b)
	accessors := map[string]func(){
		"ID":         func() { _ = c.ID() },
		"Name":       func() { _ = c.Name() },
		"Image":      func() { _ = c.Image() },
		"ReplicaSet": func() { _ = c.ReplicaSet() },
		"Port":       func() { _ = c.Port() },
		"Host":       func() { _ = c.Host() },
	}
	for name, accessor := range accessors {
		b.Run(name, func(b *testing.B) {
			b.ReportAllocs()
			for b.Loop() {
				accessor()
			}
		})
	}
}

func BenchmarkOptions(b *testing.B) {
	options := map[string]Option{
		"WithImage":        WithImage("mongo:8.0-noble"),
		"WithReplicaSet":   WithReplicaSet("rs0"),
		"WithTLS":          WithTLS(),
		"WithPort":         WithPort(27017),
		"WithLogger":       WithLogger(dockerclient.NopLogger()),
		"WithDocker":       WithDocker(dockermock.NewFake()),
		"WithStartTimeout": WithStartTimeout(90 * time.Second),
		"WithLabel":        WithLabel("suite", "checkout"),
		"WithMongodArgs":   WithMongodArgs("--setParameter", "enableTestCommands=1"),
		"WithName":         WithName("mongotest-bench"),
		"WithHostIP":       WithHostIP("172.17.0.1"),
	}
	for name, option := range options {
		b.Run(name, func(b *testing.B) {
			cfg := newConfig()
			b.ReportAllocs()
			for b.Loop() {
				option(&cfg)
			}
		})
	}
}

func BenchmarkContainerConfig(b *testing.B) {
	cfg := newConfig()
	WithReplicaSet("rs0")(&cfg)
	WithMongodArgs("--setParameter", "enableTestCommands=1")(&cfg)
	WithLabel("suite", "checkout")(&cfg)
	if err := cfg.finalise(); err != nil {
		b.Fatal(err)
	}
	b.ReportAllocs()
	for b.Loop() {
		_ = cfg.containerConfig()
	}
}

func BenchmarkGetAvailablePort(b *testing.B) {
	b.ReportAllocs()
	for b.Loop() {
		if _, err := GetAvailablePort(); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkDetectContainerisation(b *testing.B) {
	// This one touches the filesystem, which is why it is on the start path
	// exactly once and never on a per-connection path.
	b.ReportAllocs()
	for b.Loop() {
		_ = DetectContainerisation()
	}
}

func BenchmarkResolveHost(b *testing.B) {
	hosts := map[string]hostResolver{
		"unix socket on a host": {
			dockerHost: "unix:///var/run/docker.sock",
			getenv:     func(string) string { return "" },
			detect:     func() Containerisation { return Containerisation{Signal: "none"} },
			gateway:    func() (string, error) { return "", errNoDefaultRoute },
		},
		"tcp daemon": {
			dockerHost: "tcp://build-host:2376",
			getenv:     func(string) string { return "" },
			detect:     func() Containerisation { return Containerisation{Signal: "none"} },
			gateway:    func() (string, error) { return "", errNoDefaultRoute },
		},
		"sibling containers": {
			dockerHost: "unix:///var/run/docker.sock",
			getenv:     func(string) string { return "" },
			detect:     func() Containerisation { return Containerisation{Containerised: true, Signal: "/.dockerenv exists"} },
			gateway:    func() (string, error) { return "172.17.0.1", nil },
		},
		"explicit override": {
			dockerHost: "unix:///var/run/docker.sock",
			override:   "10.1.2.3",
			getenv:     func(string) string { return "" },
			detect:     func() Containerisation { return Containerisation{Signal: "none"} },
			gateway:    func() (string, error) { return "", errNoDefaultRoute },
		},
	}
	for name, resolver := range hosts {
		b.Run(name, func(b *testing.B) {
			b.ReportAllocs()
			for b.Loop() {
				if _, err := resolver.resolve(); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

func BenchmarkParseDefaultGateway(b *testing.B) {
	routes := []byte("Iface\tDestination\tGateway \tFlags\tRefCnt\tUse\tMetric\tMask\t\tMTU\tWindow\tIRTT\n" +
		"eth0\t000011AC\t00000000\t0001\t0\t0\t0\t0000FFFF\t0\t0\t0\n" +
		"eth0\t00000000\t010011AC\t0003\t0\t0\t0\t00000000\t0\t0\t0\n")
	b.ReportAllocs()
	for b.Loop() {
		if _, err := parseDefaultGateway(routes); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkReadyProbe(b *testing.B) {
	fake, port := benchDaemon(b)
	ctx := context.Background()
	c, err := Start(ctx, WithDocker(fake), WithPort(port))
	if err != nil {
		b.Fatal(err)
	}
	b.Cleanup(func() { _ = c.Stop(ctx) })
	probe := readyProbe{
		docker: fake, logger: dockerclient.NopLogger(),
		id: c.ID(), name: c.Name(), host: c.Host(), port: c.Port(), timeout: 5 * time.Second,
	}
	b.ReportAllocs()
	for b.Loop() {
		if err := probe.wait(ctx); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkErrorMessages(b *testing.B) {
	const id = "3f1a9c4b7e2d5a8f0b6c3e9d1a4f7b2c5e8d0a3f6b9c2e5d8a1f4b7c0e3d6a9f"
	errs := map[string]error{
		"StartError":           &StartError{Op: "create container", Name: "mongotest-1a2b3c4d", Image: defaultImage, Err: dockerclient.ErrNotFound},
		"UnpublishedPortError": &UnpublishedPortError{Port: mongoPort, Name: "mongotest-1a2b3c4d", ID: id},
		"NotReadyError":        &NotReadyError{Name: "mongotest-1a2b3c4d", ID: id, Host: loopback, Port: 32768, Timeout: defaultStartTimeout, Cause: context.DeadlineExceeded},
		"UnresolvedHostError":  &UnresolvedHostError{DockerHost: "unix:///var/run/docker.sock", Signal: "/.dockerenv exists", Err: errNoDefaultRoute},
	}
	for name, err := range errs {
		b.Run(name, func(b *testing.B) {
			b.ReportAllocs()
			for b.Loop() {
				_ = err.Error()
			}
		})
	}
}

func BenchmarkShortID(b *testing.B) {
	const id = "3f1a9c4b7e2d5a8f0b6c3e9d1a4f7b2c5e8d0a3f6b9c2e5d8a1f4b7c0e3d6a9f"
	b.ReportAllocs()
	for b.Loop() {
		_ = shortID(id)
	}
}

func BenchmarkPublishedPorts(b *testing.B) {
	info := dockerclient.ContainerInspect{
		NetworkSettings: dockerclient.NetworkSettings{Ports: map[string][]dockerclient.PortBinding{
			mongoPort: {{HostIP: loopback, HostPort: strconv.Itoa(32768)}},
		}},
	}
	b.ReportAllocs()
	for b.Loop() {
		_ = publishedPorts(info)
	}
}
