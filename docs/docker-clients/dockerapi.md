---
title: dockerapi
description: A Docker Engine API client built on the Go standard library alone.
---

# dockerapi

`github.com/tophergopher/mongotest/dockerapi` is the default implementation of
[`dockerclient.Client`](index.md). It has no third-party dependencies, and
implements exactly the endpoints mongotest needs.

## Getting a client

```go
c, err := dockerapi.FromEnv()
if err != nil {
    return err // the message names the variable or setting to fix
}
```

`FromEnv` locates the daemon the way the `docker` CLI does, in this order:

1. `DOCKER_HOST`, if set.
2. `DOCKER_CONTEXT`, or the `currentContext` recorded in the Docker config
   directory (`DOCKER_CONFIG` or `~/.docker`), resolved through the context's
   `meta.json`.
3. With nothing configured, a probe of the well-known sockets, taking the
   first that answers:

    ```
    $XDG_RUNTIME_DIR/docker.sock          rootless Docker
    /var/run/docker.sock                  system Docker
    $XDG_RUNTIME_DIR/podman/podman.sock   rootless Podman
    /run/podman/podman.sock               system Podman
    ```

    `/run/user/$UID` stands in when `XDG_RUNTIME_DIR` is unset. On macOS the
    `podman machine` sockets are searched too. On Windows the optional Docker
    Desktop TCP endpoint is probed, then the AF_UNIX socket `podman machine`
    exposes.

The probe connects rather than checking that the file exists, so a socket
left behind by a stopped daemon never shadows a running one. When nothing
answers, `FromEnv` still returns a client pointed at the Docker default, and
the failure surfaces on the first call as a `ConnectionError` naming the
socket. Building a client never depends on a daemon being up.

## Options

`New` takes options for a fixed host and more:

```go
c, err := dockerapi.New(
    dockerapi.WithHost("tcp://build-host:2376"),
    dockerapi.WithTLSConfig(&tls.Config{RootCAs: pool}),
    dockerapi.WithAPIVersion("1.41"),
    dockerapi.WithUserAgent("my-tests/1.0"),
    dockerapi.WithLogger(slog.Default()),
)
```

| Option | Effect |
| --- | --- |
| `WithHost(h)` | `unix:///path`, `tcp://host:port`, `http://` or `https://`. A host without a scheme is refused before anything is dialled. Skips discovery. |
| `WithHTTPClient(hc)` | Supply your own `*http.Client`. Useful for an in-process stub in unit tests. |
| `WithTLSConfig(cfg)` | Trust a daemon's CA explicitly. Without it, `DOCKER_CERT_PATH` is used when `DOCKER_TLS_VERIFY` is set. |
| `WithAPIVersion(v)` | Pin the API version instead of negotiating. |
| `WithUserAgent(ua)` | Sent on every request. Defaults to `mongotest-dockerapi/1`. |
| `WithLogger(l)` | One `Debug` record per request. `*slog.Logger` satisfies the interface. |

## Version negotiation

Negotiation is mandatory with the daemons in the wild: Podman's compatible API
topped out at 1.41 until 5.8, and recent Docker Engines accept 1.24 through
1.5x but are stricter about what each version allows.

`dockerapi` asks for its preferred version (1.44) and accepts anything down to
1.24. The first request to a daemon triggers a `GET /version`; the client then
uses the highest version both sides support. Call `Negotiate` to do it
eagerly, and `APIVersion` to read the result:

```go
if err := c.Negotiate(ctx); err != nil {
    var ve *dockerclient.APIVersionError
    if errors.As(err, &ve) {
        // ve names the product (docker or podman) and both version windows
    }
}
fmt.Println(c.APIVersion()) // for example "1.44"
```

## Which engine answered

```go
switch c.Runtime() {
case dockerapi.RuntimeDocker:
case dockerapi.RuntimePodman:
case dockerapi.RuntimeUnknown: // not yet negotiated, or an engine that is neither
}
fmt.Println(c.ServerProduct()) // how the daemon described itself
```

Podman is identified by the `Libpod-API-Version` header it sets on every
response, falling back to the component list in `GET /version`.

## Operations

All methods of the [interface](index.md#the-interface) are implemented, plus
two conveniences:

```go
// ExecStart buffers stdout and stderr; ExecStartTo streams them.
stdout, stderr, err := c.ExecStart(ctx, execID)

// CopyArchiveToContainer takes a tar stream you already have.
err = c.CopyArchiveToContainer(ctx, id, "/etc/mongo-tls", tarReader)
```

A few behaviours worth knowing:

- **`ImagePull`** drains the progress stream and surfaces an `error` line
  from the daemon as a `*PullError`, so a failed pull is an error and not a
  silent half-image.
- **`CopyToContainer`** builds the tar archive with `archive/tar` and streams
  it through an `io.Pipe`; nothing is buffered beyond the files you passed.
  It works on a container that has been created but not yet started, which is
  how TLS material will be placed before `mongod` runs.
- **`Exec`** captures stdout and stderr separately through the multiplexed
  stream, waits for the process to exit, and reports a non-zero exit code in
  `ExecResult.ExitCode` rather than as an error.
- **`ContainerTop`** is how you know `mongod` is actually running: a published
  port is bound before the process inside is ready.

## Unit testing code that uses dockerapi

Prefer taking a `dockerclient.Client` and using
[`dockermock`](dockermock.md). When you need a `*dockerapi.Client`
specifically, point it at `dockermock.NewDaemon()` or at an in-process
`http.RoundTripper` through `WithHTTPClient`. The package's own examples do
exactly this and run with no daemon installed.

## Footprint

Linking `dockerapi` pulls in zero third-party modules. On the shared benchmark
suite it does about three quarters of the allocations of the official client
for the same operations, with the gap widest where type translation costs
most. The numbers are recorded in the repository's `PLAN.md` and will move to
CI under issue [#50](https://github.com/TopherGopher/mongotest/issues/50).
