---
title: dockermock
description: "Test doubles for the Docker client interface: Mock, Fake and an HTTP-level Daemon."
---

# dockermock

`github.com/tophergopher/mongotest/dockermock` is the single home for test
doubles of [`dockerclient.Client`](index.md), so a project needs exactly one
of them rather than a stub per package. Three are offered, in increasing order
of how much they do for you.

| Double | Use it when |
| --- | --- |
| `Mock` | **The calls are the subject.** You want to assert which methods ran, in what order, with what arguments. |
| `Fake` | **The calls must work so something else can be tested.** You need a container lifecycle to behave, and the Docker calls are not the point. |
| `Daemon` | **The HTTP conversation is the subject.** You are testing a real client, or code that needs a `*dockerapi.Client` specifically. |

`Mock` and `Fake` satisfy `dockerclient.Client`. `Daemon` sits one level
lower: it is a fake Engine API server that a real client is pointed at.

## Mock: assert the exact calls

`Mock` has one function field per interface method and records every call.
Leave a field nil and the method succeeds with a zero result, so a test
scripts only the calls it cares about.

```go
func TestStartMongoPullsOnceWhenImageIsMissing(t *testing.T) {
    creates := 0
    docker := &dockermock.Mock{
        ContainerCreateFunc: func(ctx context.Context, name string, cfg dockerclient.ContainerConfig) (string, []string, error) {
            creates++
            if creates == 1 {
                return "", nil, dockerclient.ErrNotFound // first attempt: no image
            }
            return "c1", nil, nil
        },
    }

    id, err := StartMongo(context.Background(), docker)
    require.NoError(t, err, "a missing image must be pulled and the create retried")
    assert.Equal(t, "c1", id, "the id from the second create must be returned")

    assert.Len(t, docker.CallsTo("ImagePull"), 1, "exactly one pull must follow the not-found")
    assert.Equal(t, []string{"ContainerCreate", "ImagePull", "ContainerCreate", "ContainerStart"},
        docker.Methods(), "the sequence must be create, pull, create again, start")
}
```

| Method | Returns |
| --- | --- |
| `Calls()` | Every recorded call, in order, with method name and arguments. |
| `CallsTo(method)` | The calls to one method. |
| `Methods()` | Just the method names, in order. Handy for asserting a sequence. |
| `Reset()` | Forget the recorded calls. The function fields stay. |

## Fake: a working daemon in memory

`Fake` keeps an in-memory image and container store and behaves like a daemon:
creating a container needs its image pulled first, starting one makes it
running, inspecting it reports the ports it published, removing it makes later
calls return `ErrNotFound`. Nothing is scripted.

```go
docker := dockermock.NewFake(dockermock.WithImages("mongo:8")) // pre-pulled

id, _, err := docker.ContainerCreate(ctx, "", dockerclient.ContainerConfig{
    Image:        "mongo:8",
    ExposedPorts: map[string]struct{}{"27017/tcp": {}},
    HostConfig: &dockerclient.HostConfig{PortBindings: map[string][]dockerclient.PortBinding{
        "27017/tcp": {{HostIP: "127.0.0.1"}},
    }},
})
require.NoError(t, err)
require.NoError(t, docker.ContainerStart(ctx, id))

insp, err := docker.ContainerInspect(ctx, id)
require.NoError(t, err)
assert.True(t, insp.State.Running, "a started container must report running")
assert.NotEmpty(t, insp.HostPort("27017/tcp"), "the fake must assign a host port like a daemon would")
```

`Fake` applies the same validation as a real client, so a bad image reference
or port binding fails in the test exactly as it would in production. It also
enforces the rules that trip people up on a real daemon: removing a running
container needs `Force`, `ContainerTop` needs a running container, and
`ContainerCreate` without the image returns `ErrNotFound`.

`Containers()` returns a snapshot of the store for assertions about final
state. `Fake` passes the [conformance suite](conformance.md), so it is a
faithful stand-in within the suite's coverage.

## Daemon: a fake Docker daemon over HTTP

`Daemon` is an `httptest` server that speaks the Engine API. A real client
(`dockerapi` or `mobyclient`) is pointed at it through its `WithHost` option.
It records every request, matches routes with `{id}` placeholders, and ignores
the `/v1.xx` version prefix so tests work before and after negotiation.

```go
d := dockermock.NewDaemon()
defer d.Close()
d.ServeDefaults() // answer every endpoint with fixed, sensible data

c, err := dockerapi.New(dockerapi.WithHost(d.Host()))
require.NoError(t, err)

_, err = c.ImageInspect(ctx, "mongo:8")
require.NoError(t, err)

reqs := d.RequestsTo("GET", "/images/mongo:8/json")
require.Len(t, reqs, 1, "one inspect request must reach the daemon")
```

| Method | Purpose |
| --- | --- |
| `NewDaemon(opts...)` | Start listening on a unix socket, or on TCP with `OverTCP()`. |
| `Host()` / `URL()` | The address in `DOCKER_HOST` form, and as an HTTP URL. |
| `Handle(method, pattern, h)` | Register or replace a route. Patterns take `{id}` placeholders; `PathParam(r, "id")` reads them. |
| `ServeDefaults()` | Fixed answers for every endpoint. |
| `ServeVersion(api, min)` / `ServePodmanVersion(api, libpod)` | Script `GET /version` to look like a given Docker or Podman release. |
| `ServeFake(f)` | Back the HTTP routes with a `Fake`, so a real client drives a working in-memory daemon end to end. |
| `Requests()` / `RequestsTo(method, path)` | What arrived, including bodies. |
| `Reset()` | Forget recorded requests. Routes stay. |

Helpers for writing handlers: `JSON(w, status, v)`, `Error(w, status, msg)`
for the daemon's error envelope, and `Frame(stream, payload)` and
`Hijack(w, stream)` for producing multiplexed exec output over a hijacked
connection.

### Scripting an error

```go
d.Handle("POST", "/containers/create", func(w http.ResponseWriter, r *http.Request) {
    dockermock.Error(w, http.StatusConflict, "Conflict. The container name \"/mongo\" is already in use")
})

_, _, err := c.ContainerCreate(ctx, "mongo", cfg)
assert.ErrorIs(t, err, dockerclient.ErrConflict, "a 409 must be classified as a conflict")
```

### Looking like Podman

```go
d.ServePodmanVersion("1.41", "4.9.0")
c, _ := dockerapi.New(dockerapi.WithHost(d.Host()))
require.NoError(t, c.Negotiate(ctx))
assert.Equal(t, dockerapi.RuntimePodman, c.Runtime(), "the Libpod header must identify Podman")
assert.Equal(t, "1.41", c.APIVersion(), "the client must negotiate down to Podman's cap")
```

## Choosing

```
Mock    the calls are the subject
Fake    the calls must work so something else can be tested
Daemon  the HTTP conversation is the subject
```

When in doubt, start with `Fake`. Reach for `Mock` when a test starts caring
about order or count, and for `Daemon` only when the thing under test is a
client.
