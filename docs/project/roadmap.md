---
title: Roadmap
description: Where mongotest is going, what is done, and what is next.
---

# Roadmap

mongotest is in the middle of a deliberate rework. The goal is a smaller,
driver-neutral core with a clean API, without breaking the calls people use
today. This page summarises the plan; the full version with its findings lives
in [`PLAN.md`](https://github.com/TopherGopher/mongotest/blob/master/PLAN.md),
and the work is tracked under issue
[#48](https://github.com/TopherGopher/mongotest/issues/48).

## Goals

1. **Remove the easymongo dependency** from the core. mongotest depends only
   on the official MongoDB Go driver; easymongo depends on mongotest, not the
   other way round.
2. **Support driver v1 and v2** following the driver's own convention: the
   root module for v1, a nested `v2/` module for v2.
3. **Radically lighten dependencies.** Talk to the Docker Engine API with the
   standard library only. No `github.com/docker/docker`, no `moby`, no logrus,
   no mongo-tools in the core modules.
4. **Latest Go, latest actions, latest packages.**
5. **Test-driven**, with a runnable example and a benchmark for every exported
   function.

## Done

The Docker layer is finished and is what the rest will be built on.

| Package | State |
| --- | --- |
| [`dockerclient`](../docker-clients/index.md) | The interface, shared types, error sentinels and validation. Standard library only. |
| [`dockerapi`](../docker-clients/dockerapi.md) | The standard-library Engine API client, and the default. Daemon discovery, version negotiation, Podman detection. |
| [`dockermock`](../docker-clients/dockermock.md) | `Mock`, `Fake` and an HTTP-level `Daemon`. |
| [`dockerclienttest`](../docker-clients/conformance.md) | Conformance and benchmark suites every implementation runs. |
| [`mobyclient`](../docker-clients/mobyclient.md) | Wraps the official client. Own module. Passes the same conformance suite. |

## Next, in order

1. **`mongod`** ([#32](https://github.com/TopherGopher/mongotest/issues/32)):
   a driver-free package that owns the container lifecycle. Everything else
   waits on this.
2. Reaper and signal handling
   ([#33](https://github.com/TopherGopher/mongotest/issues/33)), TLS
   ([#34](https://github.com/TopherGopher/mongotest/issues/34)), `mongosh`
   exec ([#35](https://github.com/TopherGopher/mongotest/issues/35)).
3. Logging behind a small interface
   ([#36](https://github.com/TopherGopher/mongotest/issues/36)), then logrus,
   zap and zerolog adapters as isolated modules.
4. The driver v1 root package
   ([#40](https://github.com/TopherGopher/mongotest/issues/40),
   [#41](https://github.com/TopherGopher/mongotest/issues/41)), then the v2
   module ([#42](https://github.com/TopherGopher/mongotest/issues/42)), then
   the exporter module ([#43](https://github.com/TopherGopher/mongotest/issues/43)).
5. Remove the old root implementation and the heavy dependencies
   ([#44](https://github.com/TopherGopher/mongotest/issues/44)).
6. Toolchain, workspace, CI and release tooling
   ([#45](https://github.com/TopherGopher/mongotest/issues/45) to
   [#47](https://github.com/TopherGopher/mongotest/issues/47)).

Opened along the way and not yet started: benchmarks in CI with regression
tracking ([#50](https://github.com/TopherGopher/mongotest/issues/50)), shared
daemon discovery across implementations
([#51](https://github.com/TopherGopher/mongotest/issues/51)), and resolving a
reachable host instead of assuming loopback, which matters for
docker-in-docker ([#52](https://github.com/TopherGopher/mongotest/issues/52)).

## The API this is heading for

Identical in the root package (driver v1 types) and in `v2` (driver v2 types).

```go
// Primary API
func Start(ctx context.Context, opts ...Option) (*Instance, error)
func Run(tb testing.TB, opts ...Option) *Instance          // tb.Fatal on error, tb.Cleanup(Stop)
func Attach(ctx context.Context, uri string) (*Instance, error) // no container; an existing server

type Instance struct {
    *mongo.Client               // promoted: inst.Database("x").Collection("y")
    Container *mongod.Container // nil for Attach
}
func (i *Instance) URI() string
func (i *Instance) Stop(ctx context.Context) error // idempotent
func (i *Instance) Exec(ctx context.Context, cmd ...string) (mongod.ExecResult, error)
func (i *Instance) RunScript(ctx context.Context, js string) (string, error) // mongosh --quiet --norc --eval

// Options
WithImage("mongo:8.0")  WithReplicaSet("rs0")  WithTLS()  WithPort(27018)
WithLogger(l)  WithDocker(dockerclient.Client)  WithStartTimeout(d)
WithLabel(k, v)  WithMongodArgs("--setParameter", "...")
```

The test you write today:

```go
conn, err := mongotest.NewTestConnection(true)
require.NoError(t, err)
t.Cleanup(func() { _ = conn.KillMongoContainer() })
```

becomes:

```go
inst := mongotest.Run(t)
```

and `inst` is a `*mongo.Client` with a container attached.

### What stays

Every call documented on this site is kept as a plain wrapper over the new
API, not marked deprecated: `NewTestConnection`, `NewReplicaSetContainer`,
`MongoClientWithContainer`, `TestConnection` (as an alias of `Instance`),
`KillMongoContainer`, `MongoContainerID`, `RunMongoScriptOnContainer`,
`ExecCommandInMongoContainer` and `GetAvailablePort`. `EasyMongoWithContainer`
moves to easymongo, which will depend on mongotest.

### What changes underneath

- **Replica sets** are initiated through the driver with the requested name,
  then polled until the member reports itself writable primary.
- **TLS** generates a CA and a server certificate, copies them into the
  created container before start, and runs `mongod` with `--tlsMode
  requireTLS`. `Container.TLSConfig()` and `CAPEM()` are exposed.
- **Scripts** run with `mongosh`, which the current images actually ship.
- **The signal handler** is installed on the first container start, reaps,
  then resets the signal and re-raises it, so ++ctrl+c++ terminates the
  process as expected.
- **The default image** becomes `mongo:8`, overridable by `WithImage` and by
  `MONGOTEST_IMAGE`.
- **Logging** goes through a four-method `Logger` interface satisfied by
  `*slog.Logger`, defaulting to discard.

## Following along

Watch the [issues](https://github.com/TopherGopher/mongotest/issues) and the
pull requests against `master`. Each step lands as its own change with tests
first, so the shape of the API can be seen in the test files before the
implementation is complete.
