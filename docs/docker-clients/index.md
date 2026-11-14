---
title: Docker clients
description: The dockerclient interface, its two implementations and its test doubles.
status: current
---

# Docker clients

mongotest only needs a handful of Docker Engine operations: pull an image,
create and start a container, copy files into it, run a command inside it,
inspect it, remove it. Rather than tying the whole library to one Docker SDK,
those operations are declared as an interface, and you choose the
implementation.

```
dockerclient   the interface, the shared types and the error sentinels.
               Standard library only. Everything else points at this.
   |
   +-- dockerapi    standard-library implementation (the default)
   +-- mobyclient   own module, wraps github.com/moby/moby/client
   +-- dockermock   test doubles: Mock, Fake and an HTTP-level Daemon
```

These packages are public and useful on their own. If you need to drive Docker
from Go without pulling in the official SDK's dependency tree, `dockerapi` is a
complete, tested client for the endpoints above.

<div class="grid cards" markdown>

-   :material-code-braces:{ .lg .middle } **[dockerapi](dockerapi.md)**

    ---

    Zero third-party dependencies. Discovers the daemon like the `docker`
    CLI, negotiates the API version, works with Docker and Podman.

-   :material-docker:{ .lg .middle } **[mobyclient](mobyclient.md)**

    ---

    Wraps the official `moby/moby/client`. A separate module, so its
    dependency tree only reaches builds that ask for it.

-   :material-test-tube:{ .lg .middle } **[dockermock](dockermock.md)**

    ---

    `Mock` records calls, `Fake` is an in-memory daemon, `Daemon` is a fake
    Engine API over HTTP. One home for all test doubles.

-   :material-check-decagram:{ .lg .middle } **[Conformance suite](conformance.md)**

    ---

    One test suite and one benchmark suite that every implementation runs,
    so they are held to the same behaviour.

</div>

## The interface

```go
type Client interface {
    Host() string

    ImagePull(ctx context.Context, ref string) error
    ImageInspect(ctx context.Context, ref string) (ImageInspect, error)

    ContainerCreate(ctx context.Context, name string, cfg ContainerConfig) (id string, warnings []string, err error)
    ContainerStart(ctx context.Context, id string) error
    ContainerRemove(ctx context.Context, id string, opts RemoveOptions) error
    ContainerInspect(ctx context.Context, id string) (ContainerInspect, error)
    ContainerTop(ctx context.Context, id string) (Top, error)

    CopyToContainer(ctx context.Context, id, destDir string, files []File) error
    CopyArchiveToContainer(ctx context.Context, id, destDir string, archive io.Reader) error

    ExecCreate(ctx context.Context, containerID string, cfg ExecConfig) (execID string, err error)
    ExecStartTo(ctx context.Context, execID string, stdout, stderr io.Writer) error
    ExecInspect(ctx context.Context, execID string) (ExecInspect, error)
    Exec(ctx context.Context, containerID string, cmd ...string) (ExecResult, error)
}
```

Implementations must be safe for concurrent use; mongotest drives one client
from parallel tests.

## Writing code against it

Take a `dockerclient.Client` and let the caller decide which one it is:

```go
func StartMongo(ctx context.Context, docker dockerclient.Client) (string, error) {
    cfg := dockerclient.ContainerConfig{
        Image:        "mongo:8",
        ExposedPorts: map[string]struct{}{"27017/tcp": {}},
        HostConfig: &dockerclient.HostConfig{
            PortBindings: map[string][]dockerclient.PortBinding{
                "27017/tcp": {{HostIP: "127.0.0.1"}}, // empty HostPort: the daemon picks one
            },
        },
    }
    id, _, err := docker.ContainerCreate(ctx, "", cfg)
    if dockerclient.IsNotFound(err) {
        // The image is not local. Pull once and try again.
        if err = docker.ImagePull(ctx, cfg.Image); err != nil {
            return "", err
        }
        id, _, err = docker.ContainerCreate(ctx, "", cfg)
    }
    if err != nil {
        return "", err
    }
    return id, docker.ContainerStart(ctx, id)
}
```

In production pass `dockerapi.FromEnv()`. In a unit test pass
`dockermock.NewFake()` and the same code runs against an in-memory daemon:

```go
func TestStartMongoPullsWhenMissing(t *testing.T) {
    fake := dockermock.NewFake()
    id, err := StartMongo(context.Background(), fake)
    require.NoError(t, err, "a missing image must be pulled, not reported")

    insp, err := fake.ContainerInspect(context.Background(), id)
    require.NoError(t, err)
    assert.True(t, insp.State.Running, "the container must be running after StartMongo")
}
```

## Shared types

`dockerclient` owns every type that crosses the interface, so code written
against it never imports an implementation:

| Type | Purpose |
| --- | --- |
| `ContainerConfig`, `HostConfig`, `PortBinding` | What to create. Unset fields take the daemon's defaults. |
| `ContainerInspect`, `ContainerState`, `NetworkSettings` | What came back. `ContainerInspect.HostPort("27017/tcp")` finds the published port. |
| `Top` | The processes in a container. The only reliable way to know `mongod` itself is up. |
| `File` | A name, mode and content to copy in. |
| `ExecConfig`, `ExecInspect`, `ExecResult` | Running a command and reading its result. A non-zero exit code is data, not an error. |
| `Logger` | Four methods, satisfied by `*slog.Logger`. `NopLogger()` discards. |

## Errors

Every implementation reports the same sentinels for the same conditions, so
`errors.Is(err, dockerclient.ErrNotFound)` means the same thing whichever
client is plugged in. The [errors page](errors.md) lists them.
