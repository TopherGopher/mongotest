---
title: mobyclient
description: A dockerclient.Client backed by the official moby client, in its own module.
---

# mobyclient

`github.com/tophergopher/mongotest/mobyclient` wraps the official
`github.com/moby/moby/client` so it satisfies
[`dockerclient.Client`](index.md). Use it when your project already depends on
the official client and you want one Docker connection pool, one set of
options and one TLS configuration across your code and mongotest.

It is a **separate Go module**, so its dependency tree only reaches builds
that import it:

```sh
go get github.com/tophergopher/mongotest/mobyclient
```

## Getting a client

```go
c, err := mobyclient.FromEnv()          // discover the daemon like the docker CLI
c, err := mobyclient.New(               // or configure it explicitly
    mobyclient.WithHost("unix:///var/run/docker.sock"),
    mobyclient.WithLogger(slog.Default()),
)
```

| Option | Effect |
| --- | --- |
| `WithHost(h)` | Daemon address in `DOCKER_HOST` form. Skips discovery. |
| `WithMobyClient(cli)` | Wrap a `*client.Client` you already built, for options this package does not expose (custom `*tls.Config`, extra headers). `WithHost` and `WithLogger` are ignored when this is set, since that client is already configured. |
| `WithLogger(l)` | One `Debug` record per request through the moby client's response hooks. |

## Error mapping

The moby client has its own error types. `mobyclient` maps them onto the
`dockerclient` sentinels so that calling code does not care which
implementation it got:

| moby condition | reported as |
| --- | --- |
| not found | `dockerclient.ErrNotFound` (a `*StatusError`) |
| conflict | `dockerclient.ErrConflict` |
| unauthorized or forbidden | `dockerclient.ErrUnauthorized` |
| connection failure | `dockerclient.ErrConnectionFailed` (a `*ConnectionError`) |
| anything else from the daemon | `dockerclient.ErrDaemonResponse` (a `*ResponseError`) |

Context cancellation and deadline errors pass through untouched.

## Same behaviour, verified

`mobyclient` runs the same [conformance suite](conformance.md) as
`dockerapi`, against the same `dockermock.Daemon`, so the two are
interchangeable in every respect the suite covers: request shape, error
classification, streaming, exec demultiplexing and validation.

## Choosing between dockerapi and mobyclient

| | `dockerapi` | `mobyclient` |
| --- | --- | --- |
| Third-party modules linked | 0 | 18 |
| `go.sum` lines added to a consumer | 4 | 57 |
| Allocations on the shared benchmarks | 1.0x | about 1.35x |
| Features beyond the interface | version negotiation, runtime detection, socket probing | everything the official client offers, via `WithMobyClient` |
| Pick it when | you want the smallest footprint, or you run against Podman | you already use the official client and want to share its configuration |

Both numbers come from the repository's recorded baseline in `PLAN.md`.

## A note on the module

Until a tagged release contains `dockerclient`, `mobyclient/go.mod` carries a
`replace` directive pointing at the parent directory. It is annotated in the
file and removed by issue [#47](https://github.com/TopherGopher/mongotest/issues/47).
