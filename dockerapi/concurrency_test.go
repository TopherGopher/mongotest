package dockerapi

import (
	"context"
	"fmt"
	"net/http"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/tophergopher/mongotest/internal/fakedaemon"
)

// containerFake wires a fake daemon that behaves like a tiny container store
// so many callers can create, start, inspect and remove concurrently.
func containerFake(t *testing.T, fd *fakedaemon.Server) *int64 {
	var created int64
	var mu sync.Mutex
	live := map[string]bool{}
	fd.ServeVersion("1.54", "1.40")
	fd.Handle("POST", "/containers/create", func(w http.ResponseWriter, r *http.Request) {
		n := atomic.AddInt64(&created, 1)
		id := fmt.Sprintf("c%03d", n)
		mu.Lock()
		live[id] = true
		mu.Unlock()
		fakedaemon.JSON(w, 201, map[string]any{"Id": id})
	})
	fd.Handle("POST", "/containers/{id}/start", func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		ok := live[fakedaemon.PathParam(r, "id")]
		mu.Unlock()
		if !ok {
			fakedaemon.Error(w, 404, "No such container")
			return
		}
		w.WriteHeader(204)
	})
	fd.Handle("GET", "/containers/{id}/json", func(w http.ResponseWriter, r *http.Request) {
		id := fakedaemon.PathParam(r, "id")
		mu.Lock()
		ok := live[id]
		mu.Unlock()
		if !ok {
			fakedaemon.Error(w, 404, "No such container")
			return
		}
		fakedaemon.JSON(w, 200, map[string]any{"Id": id, "State": map[string]any{"Running": true}})
	})
	fd.Handle("DELETE", "/containers/{id}", func(w http.ResponseWriter, r *http.Request) {
		id := fakedaemon.PathParam(r, "id")
		mu.Lock()
		ok := live[id]
		delete(live, id)
		mu.Unlock()
		if !ok {
			fakedaemon.Error(w, 404, "No such container")
			return
		}
		w.WriteHeader(204)
	})
	return &created
}

func lifecycle(t *testing.T, c *Client, i int) {
	t.Helper()
	ctx := context.Background()
	id, _, err := c.ContainerCreate(ctx, fmt.Sprintf("par-%d", i), ContainerConfig{Image: "mongo:8"})
	if err != nil {
		t.Fatal(err)
	}
	if err := c.ContainerStart(ctx, id); err != nil {
		t.Fatal(err)
	}
	info, err := c.ContainerInspect(ctx, id)
	if err != nil || info.ID != id || !info.State.Running {
		t.Fatalf("inspect %s: %+v %v", id, info, err)
	}
	if err := c.ContainerRemove(ctx, id, RemoveOptions{Force: true}); err != nil {
		t.Fatal(err)
	}
	if _, err := c.ContainerInspect(ctx, id); !IsNotFound(err) {
		t.Fatalf("after remove: %v", err)
	}
}

// One shared client driven from many goroutines: negotiation must happen
// exactly once and every lifecycle must complete.
func TestConcurrentGoroutinesShareOneClient(t *testing.T) {
	fd := fakedaemon.New(t)
	created := containerFake(t, fd)
	c, err := New(WithHost(fd.Host()))
	if err != nil {
		t.Fatal(err)
	}
	const n = 40
	var wg sync.WaitGroup
	errs := make(chan error, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			ctx := context.Background()
			id, _, err := c.ContainerCreate(ctx, "", ContainerConfig{Image: "mongo:8"})
			if err != nil {
				errs <- err
				return
			}
			if err := c.ContainerStart(ctx, id); err != nil {
				errs <- err
				return
			}
			if err := c.ContainerRemove(ctx, id, RemoveOptions{Force: true}); err != nil {
				errs <- err
			}
		}(i)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Error(err)
	}
	if *created != n {
		t.Fatalf("created %d containers, want %d", *created, n)
	}
	versions := 0
	for _, r := range fd.Requests() {
		if r.RawPath == "/version" {
			versions++
		}
	}
	if versions != 1 {
		t.Fatalf("/version negotiated %d times under concurrency, want 1", versions)
	}
}

// Parallel subtests, each with its own client against the same fake daemon.
func TestParallelSubtestsOwnClients(t *testing.T) {
	fd := fakedaemon.New(t)
	containerFake(t, fd)
	for i := 0; i < 12; i++ {
		i := i
		t.Run(fmt.Sprintf("container-%02d", i), func(t *testing.T) {
			t.Parallel()
			c, err := New(WithHost(fd.Host()))
			if err != nil {
				t.Fatal(err)
			}
			lifecycle(t, c, i)
		})
	}
}

// Parallel subtests sharing one client, over TCP for variety.
func TestParallelSubtestsSharedClientTCP(t *testing.T) {
	fd := fakedaemon.NewTCP(t)
	containerFake(t, fd)
	c, err := New(WithHost(fd.Host()))
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 12; i++ {
		i := i
		t.Run(fmt.Sprintf("container-%02d", i), func(t *testing.T) {
			t.Parallel()
			lifecycle(t, c, i)
		})
	}
}
