package dockerapi

import (
	"context"
	"fmt"
	"net/http"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tophergopher/mongotest/dockermock"
)

// containerFake wires a fake daemon that behaves like a tiny container store
// so many callers can create, start, inspect and remove concurrently.
func containerFake(fd *dockermock.Daemon) *int64 {
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
		dockermock.JSON(w, 201, createResponse{ID: id})
	})
	fd.Handle("POST", "/containers/{id}/start", func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		ok := live[dockermock.PathParam(r, "id")]
		mu.Unlock()
		if !ok {
			dockermock.Error(w, 404, "No such container")
			return
		}
		w.WriteHeader(204)
	})
	fd.Handle("GET", "/containers/{id}/json", func(w http.ResponseWriter, r *http.Request) {
		id := dockermock.PathParam(r, "id")
		mu.Lock()
		ok := live[id]
		mu.Unlock()
		if !ok {
			dockermock.Error(w, 404, "No such container")
			return
		}
		dockermock.JSON(w, 200, ContainerInspect{ID: id, State: ContainerState{Running: true}})
	})
	fd.Handle("DELETE", "/containers/{id}", func(w http.ResponseWriter, r *http.Request) {
		id := dockermock.PathParam(r, "id")
		mu.Lock()
		ok := live[id]
		delete(live, id)
		mu.Unlock()
		if !ok {
			dockermock.Error(w, 404, "No such container")
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
	require.NoError(t, err, "container %d: create", i)
	require.NoError(t, c.ContainerStart(ctx, id), "container %d: start", i)
	info, err := c.ContainerInspect(ctx, id)
	require.NoError(t, err, "container %d: inspect", i)
	assert.Equal(t, id, info.ID, "container %d: inspect returns its own id, not another goroutine's", i)
	assert.True(t, info.State.Running, "container %d: reported running after start", i)
	require.NoError(t, c.ContainerRemove(ctx, id, RemoveOptions{Force: true}), "container %d: remove", i)
	_, err = c.ContainerInspect(ctx, id)
	assert.True(t, IsNotFound(err), "container %d: inspect after remove must be not found, got %v", i, err)
}

// One shared client driven from many goroutines: negotiation must happen
// exactly once and every lifecycle must complete.
func TestConcurrentGoroutinesShareOneClient(t *testing.T) {
	fd := newDaemon(t)
	created := containerFake(fd)
	c, err := New(WithHost(fd.Host()))
	require.NoError(t, err, "client construction")
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
				errs <- fmt.Errorf("goroutine %d create: %w", i, err)
				return
			}
			if err := c.ContainerStart(ctx, id); err != nil {
				errs <- fmt.Errorf("goroutine %d start: %w", i, err)
				return
			}
			if err := c.ContainerRemove(ctx, id, RemoveOptions{Force: true}); err != nil {
				errs <- fmt.Errorf("goroutine %d remove: %w", i, err)
			}
		}(i)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		assert.NoError(t, err, "every concurrent lifecycle must complete")
	}
	assert.EqualValues(t, n, atomic.LoadInt64(created), "every goroutine must have created exactly one container")
	assert.Equal(t, 1, versionCalls(fd), "/version must be negotiated exactly once even under concurrent first use")
}

// Parallel subtests, each with its own client against the same fake daemon.
func TestParallelSubtestsOwnClients(t *testing.T) {
	fd := newDaemon(t)
	containerFake(fd)
	for i := 0; i < 12; i++ {
		t.Run(fmt.Sprintf("container-%02d", i), func(t *testing.T) {
			t.Parallel()
			c, err := New(WithHost(fd.Host()))
			require.NoError(t, err, "client construction in parallel subtest %d", i)
			lifecycle(t, c, i)
		})
	}
}

// Parallel subtests sharing one client, over TCP for variety.
func TestParallelSubtestsSharedClientTCP(t *testing.T) {
	fd := newDaemon(t, dockermock.OverTCP())
	containerFake(fd)
	c, err := New(WithHost(fd.Host()))
	require.NoError(t, err, "client construction over tcp")
	for i := 0; i < 12; i++ {
		t.Run(fmt.Sprintf("container-%02d", i), func(t *testing.T) {
			t.Parallel()
			lifecycle(t, c, i)
		})
	}
}
