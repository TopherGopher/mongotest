---
title: Start, Run and Attach
description: "The root package API specified in #40: Run(t) returns a driver client with a container attached."
status: planned
tracks: ["#40", "#41", "#42", "#36"]
---

# Start, Run and Attach

The root `mongotest` package becomes a thin driver layer over
[`mongod`](mongod.md). Three constructors, one type, and options re-exported
so a test imports one package.

```go
func Start(ctx context.Context, opts ...Option) (*Instance, error)
func Run(tb testing.TB, opts ...Option) *Instance
func Attach(ctx context.Context, uri string) (*Instance, error)

type Instance struct {
    *mongo.Client               // promoted: inst.Database("x").Collection("y")
    Container *mongod.Container // nil for Attach
}

func (i *Instance) URI() string
func (i *Instance) Stop(ctx context.Context) error // disconnect, remove, idempotent
func (i *Instance) Exec(ctx context.Context, cmd ...string) (dockerclient.ExecResult, error)
func (i *Instance) RunScript(ctx context.Context, js string) (string, error)
```

## `Run`: the one most tests want

```go
func TestInsertAndFind(t *testing.T) {
    inst := mongotest.Run(t)

    coll := inst.Database("heroes").Collection("roster")
    _, err := coll.InsertOne(t.Context(), hero{Name: "Diana"})
    require.NoError(t, err, "inserting into a fresh collection must succeed")
}
```

`Run` calls `Start` with a context derived from the test's deadline, fails the
test on error, and registers `tb.Cleanup` to stop the instance. There is
nothing to remember. `inst` is a `*mongo.Client`, so every driver method is
right there.

## `Start`: outside a test

For a `TestMain`, a benchmark harness or a tool that is not a test:

```go
inst, err := mongotest.Start(ctx, mongotest.WithReplicaSet("rs0"))
if err != nil {
    return err
}
defer inst.Stop(context.Background())
```

`Start` runs `mongod.Start`, connects with `directConnection=true` and a short
server-selection timeout, and pings with backoff until the start timeout. In
TLS mode it applies `Container.TLSConfig()`. On any failure after the
container exists, the container is removed before the error is returned.

## `Attach`: a server you already have

```go
inst, err := mongotest.Attach(ctx, os.Getenv("MONGO_URI"))
```

No container. `Container` is nil, `Stop` only disconnects, and `Exec` and
`RunScript` return a clear error. This replaces today's
`NewTestConnection(false)` and removes its fixed address.

## Options

Re-exported from `mongod` one for one, so callers never import that package:

```go
mongotest.WithImage("mongo:8.0")     mongotest.WithReplicaSet("rs0")
mongotest.WithTLS()                  mongotest.WithPort(27018)
mongotest.WithLogger(l)              mongotest.WithDocker(client)
mongotest.WithStartTimeout(90*time.Second)
mongotest.WithLabel("suite", "orders")
mongotest.WithMongodArgs("--setParameter", "enableTestCommands=1")
```

Each follows the option, then environment, then default rule described on the
[mongod page](mongod.md#configuring-it).

## Replica sets, done properly

With `WithReplicaSet(name)`, after the first successful ping the driver layer
sends `replSetInitiate` with **the name you asked for** and the member
`localhost:27017`, tolerates "already initialized", then polls `hello` until
the member reports `isWritablePrimary`. All within the start timeout. No shell
script, no `mongo` binary, no hardcoded `cf`.

The member address is the one inside the container while the client connects
to the published port, which is why the URI keeps `directConnection=true`:
the driver must never try to follow the member list.

## Logging

[#36](https://github.com/TopherGopher/mongotest/issues/36) puts all logging
behind a four-method interface that `*slog.Logger` satisfies directly:

```go
type Logger interface {
    Debug(msg string, keysAndValues ...any)
    Info(msg string, keysAndValues ...any)
    Warn(msg string, keysAndValues ...any)
    Error(msg string, keysAndValues ...any)
}
```

The default discards everything. `WithLogger(slog.Default())` shows the
lifecycle; the adapter modules
([#37](https://github.com/TopherGopher/mongotest/issues/37) logrus,
[#38](https://github.com/TopherGopher/mongotest/issues/38) zap,
[#39](https://github.com/TopherGopher/mongotest/issues/39) zerolog) each
return a `mongotest.Logger` from the logger you already have, in their own
modules so their dependencies only reach builds that ask for them.

## Driver v2

[#42](https://github.com/TopherGopher/mongotest/issues/42) adds a nested
module, `github.com/tophergopher/mongotest/v2`, with the same API on
`go.mongodb.org/mongo-driver/v2`. Same constructors, same options, same
wrappers; only the promoted client type differs. This follows the driver's
own convention, so a project on driver v2 imports `mongotest/v2` and nothing
else changes.

## What this replaces

The [migration page](migration.md) puts each of today's calls next to its
replacement. The short version: `NewTestConnection(true)` plus a `t.Cleanup`
line becomes `Run(t)`, and the result is a driver client rather than an
easymongo connection.
