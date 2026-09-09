package dockerclienttest

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"testing"

	"github.com/tophergopher/mongotest/dockerclient"
)

// BenchFactory returns a client for a benchmark, with the test image already
// pulled where that is possible, so the measured loop is the call itself.
type BenchFactory func(b *testing.B) dockerclient.Client

// Benchmarks measures every call in the interface, so implementations can be
// compared on the same scale. Run it from each implementation:
//
//	func BenchmarkClient(b *testing.B) {
//		dockerclienttest.Benchmarks(b, newBenchClient)
//	}
//
// The numbers are the client's own cost. The daemon behind the factory
// should be a double, not a real Docker daemon, or the measurement is of
// Docker rather than of the client.
func Benchmarks(b *testing.B, newClient BenchFactory) {
	ctx := context.Background()

	// prepared returns a client with an image pulled and one running
	// container, since most calls need something to act on.
	prepared := func(b *testing.B) (dockerclient.Client, string) {
		b.Helper()
		c := newClient(b)
		if err := c.ImagePull(ctx, TestImage); err != nil {
			b.Fatalf("pulling %s: %v", TestImage, err)
		}
		id, _, err := c.ContainerCreate(ctx, "", benchConfig())
		if err != nil {
			b.Fatalf("creating a container: %v", err)
		}
		if err := c.ContainerStart(ctx, id); err != nil {
			b.Fatalf("starting %s: %v", id, err)
		}
		return c, id
	}

	b.Run("Host", func(b *testing.B) {
		c := newClient(b)
		b.ReportAllocs()
		for b.Loop() {
			_ = c.Host()
		}
	})

	b.Run("ImagePull", func(b *testing.B) {
		c := newClient(b)
		b.ReportAllocs()
		for b.Loop() {
			if err := c.ImagePull(ctx, TestImage); err != nil {
				b.Fatal(err)
			}
		}
	})

	b.Run("ImageInspect", func(b *testing.B) {
		c := newClient(b)
		if err := c.ImagePull(ctx, TestImage); err != nil {
			b.Fatal(err)
		}
		b.ReportAllocs()
		for b.Loop() {
			if _, err := c.ImageInspect(ctx, TestImage); err != nil {
				b.Fatal(err)
			}
		}
	})

	b.Run("ContainerCreate", func(b *testing.B) {
		c := newClient(b)
		if err := c.ImagePull(ctx, TestImage); err != nil {
			b.Fatal(err)
		}
		cfg := benchConfig()
		b.ReportAllocs()
		for b.Loop() {
			if _, _, err := c.ContainerCreate(ctx, "", cfg); err != nil {
				b.Fatal(err)
			}
		}
	})

	b.Run("ContainerStart", func(b *testing.B) {
		c, id := prepared(b)
		b.ReportAllocs()
		for b.Loop() {
			if err := c.ContainerStart(ctx, id); err != nil {
				b.Fatal(err)
			}
		}
	})

	b.Run("ContainerInspect", func(b *testing.B) {
		c, id := prepared(b)
		b.ReportAllocs()
		for b.Loop() {
			if _, err := c.ContainerInspect(ctx, id); err != nil {
				b.Fatal(err)
			}
		}
	})

	b.Run("ContainerInspectParallel", func(b *testing.B) {
		// Many callers sharing one client, which is how mongotest drives it
		// from parallel tests.
		c, id := prepared(b)
		b.ReportAllocs()
		b.RunParallel(func(pb *testing.PB) {
			for pb.Next() {
				if _, err := c.ContainerInspect(ctx, id); err != nil {
					b.Error(err)
					return
				}
			}
		})
	})

	b.Run("ContainerTop", func(b *testing.B) {
		c, id := prepared(b)
		b.ReportAllocs()
		for b.Loop() {
			if _, err := c.ContainerTop(ctx, id); err != nil {
				b.Fatal(err)
			}
		}
	})

	b.Run("ContainerRemove", func(b *testing.B) {
		// A fresh container per iteration, created outside the timed section.
		c := newClient(b)
		if err := c.ImagePull(ctx, TestImage); err != nil {
			b.Fatal(err)
		}
		cfg := benchConfig()
		b.ReportAllocs()
		for b.Loop() {
			b.StopTimer()
			id, _, err := c.ContainerCreate(ctx, "", cfg)
			if err != nil {
				b.Fatal(err)
			}
			b.StartTimer()
			if err := c.ContainerRemove(ctx, id, dockerclient.RemoveOptions{Force: true, RemoveVolumes: true}); err != nil {
				b.Fatal(err)
			}
		}
	})

	for _, size := range []int{1 << 10, 64 << 10} {
		b.Run(fmt.Sprintf("CopyToContainer/%dKiB", size>>10), func(b *testing.B) {
			c, id := prepared(b)
			files := []dockerclient.File{{Name: "payload", Mode: 0o644, Content: bytes.Repeat([]byte("x"), size)}}
			b.SetBytes(int64(size))
			b.ReportAllocs()
			for b.Loop() {
				if err := c.CopyToContainer(ctx, id, "/tmp", files); err != nil {
					b.Fatal(err)
				}
			}
		})
	}

	b.Run("CopyArchiveToContainer", func(b *testing.B) {
		c, id := prepared(b)
		archive := benchArchive(b, 64<<10)
		b.SetBytes(int64(len(archive)))
		b.ReportAllocs()
		for b.Loop() {
			if err := c.CopyArchiveToContainer(ctx, id, "/tmp", bytes.NewReader(archive)); err != nil {
				b.Fatal(err)
			}
		}
	})

	b.Run("ExecCreate", func(b *testing.B) {
		c, id := prepared(b)
		cfg := dockerclient.ExecConfig{Cmd: []string{"echo", "hello"}}
		b.ReportAllocs()
		for b.Loop() {
			if _, err := c.ExecCreate(ctx, id, cfg); err != nil {
				b.Fatal(err)
			}
		}
	})

	b.Run("ExecStartTo", func(b *testing.B) {
		c, id := prepared(b)
		b.ReportAllocs()
		for b.Loop() {
			b.StopTimer()
			execID, err := c.ExecCreate(ctx, id, dockerclient.ExecConfig{Cmd: []string{"echo", "hello"}})
			if err != nil {
				b.Fatal(err)
			}
			b.StartTimer()
			if err := c.ExecStartTo(ctx, execID, io.Discard, io.Discard); err != nil {
				b.Fatal(err)
			}
		}
	})

	b.Run("ExecInspect", func(b *testing.B) {
		c, id := prepared(b)
		execID, err := c.ExecCreate(ctx, id, dockerclient.ExecConfig{Cmd: []string{"echo", "hello"}})
		if err != nil {
			b.Fatal(err)
		}
		b.ReportAllocs()
		for b.Loop() {
			if _, err := c.ExecInspect(ctx, execID); err != nil {
				b.Fatal(err)
			}
		}
	})

	b.Run("Exec", func(b *testing.B) {
		// Create, start and inspect: what one command costs a caller.
		c, id := prepared(b)
		b.ReportAllocs()
		for b.Loop() {
			if _, err := c.Exec(ctx, id, "echo", "hello"); err != nil {
				b.Fatal(err)
			}
		}
	})
}

// benchConfig is the container every benchmark creates: what mongod will ask
// for, so the measured encoding work is representative.
func benchConfig() dockerclient.ContainerConfig {
	return dockerclient.ContainerConfig{
		Image:        TestImage,
		Cmd:          []string{"--replSet", "rs0"},
		Labels:       map[string]string{"mongotest": "benchmark"},
		ExposedPorts: map[string]struct{}{"27017/tcp": {}},
		HostConfig: &dockerclient.HostConfig{PortBindings: map[string][]dockerclient.PortBinding{
			"27017/tcp": {{HostIP: "127.0.0.1", HostPort: ""}},
		}},
	}
}

// benchArchive builds a tar archive of the given payload size.
func benchArchive(b *testing.B, size int) []byte {
	b.Helper()
	var buf bytes.Buffer
	tw := newTarWriter(&buf)
	if err := tw.write("payload", bytes.Repeat([]byte("x"), size)); err != nil {
		b.Fatalf("building the benchmark archive: %v", err)
	}
	if err := tw.close(); err != nil {
		b.Fatalf("closing the benchmark archive: %v", err)
	}
	return buf.Bytes()
}
