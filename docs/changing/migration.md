---
title: Migrating from today's API
description: Each of today's calls next to its replacement, and what changes underneath even if you change nothing.
status: planned
tracks: ["#41", "#40"]
---

# Migrating from today's API

Nothing has to change. Every call documented in the guides is kept as a
wrapper over the new API, not marked deprecated
([#41](https://github.com/TopherGopher/mongotest/issues/41)). This page is
for when you want to move, and for understanding what moves under you if you
do not.

## Call by call

| Today | Becomes | Notes |
| --- | --- | --- |
| `conn, err := NewTestConnection(true)` | `inst := Run(t)` | `Run` fails the test and registers cleanup itself. Keep `NewTestConnection` where there is no `testing.TB`. |
| `NewTestConnection(false)` | `Attach(ctx, uri)` | The wrapper keeps the fixed `127.0.0.1:27017`; `Attach` takes any URI. |
| `t.Cleanup(func() { conn.KillMongoContainer() })` | nothing, with `Run` | `Start` callers use `defer inst.Stop(ctx)`. `KillMongoContainer` stays and calls `Stop`. |
| `NewReplicaSetContainer("rs0")` | `Run(t, WithReplicaSet("rs0"))` | The name is finally honoured. |
| `MongoClientWithContainer(func(m *mongo.Client) error)` | `Run(t)` and use `inst` directly | The wrapper stays. |
| `EasyMongoWithContainer(...)` | `easymongotest` in the easymongo repository | The only call that leaves this module. |
| `conn.D("db").C("coll")` (easymongo) | `inst.Database("db").Collection("coll")` (driver) | `TestConnection` becomes an alias of `Instance`, which promotes the driver client instead of an easymongo connection. See below. |
| `conn.MongoDriverClient()` | `inst.Client` or just `inst` | Promoted field. |
| `conn.MongoURI()` | `inst.URI()` | |
| `conn.MongoContainerID()` | `inst.Container.ID()` | Wrapper stays; empty for `Attach`. |
| `conn.RunMongoScriptOnContainer(js)` | `inst.RunScript(ctx, js)` | Runs `mongosh`, so it works on current images. |
| `conn.ExecCommandInMongoContainer([]string{...})` | `inst.Exec(ctx, "mongosh", "--eval", "...")` | Variadic; a non-zero exit is data in the result, not an error. The wrapper keeps the old error behaviour. |
| `conn.DatabaseExporter(db, coll)` | `exporter.New(inst.URI(), db, coll)` in the `exporter` module | Separate module so mongo-tools leaves the core. |
| `GetAvailablePort()` | `GetAvailablePort()` with `WithPort(p)` | Re-exported; not used on the default path. |
| `ReapRunningContainers()` | `ReapRunningContainers(ctx) error` | Now takes a context and reports failures. |

## The one thing that changes shape: the embedded connection

Today `TestConnection` embeds `*easymongo.Connection`. After
[#40](https://github.com/TopherGopher/mongotest/issues/40) it is an alias of
`Instance`, which embeds `*mongo.Client`. A test that reaches easymongo
methods through the connection stops compiling at that point:

```go
coll := conn.D("heroes").C("roster")         // easymongo: goes away
coll := conn.Database("heroes").Collection("roster") // driver: works before and after
```

The second form compiles today too, because easymongo's `Database` returns an
easymongo database with a `Collection` method, and the driver's `Database`
returns a driver database with a `Collection` method. The methods on the
collection differ, though (`Insert().One` against `InsertOne`). If you want a
test to survive the switch unchanged, write it against the driver client from
the start:

```go
client := conn.MongoDriverClient() // today
coll := client.Database("heroes").Collection("roster")
```

and later replace `conn.MongoDriverClient()` with `inst`. Projects that want
easymongo's query builder keep it by taking the `easymongotest` helpers from
the easymongo repository, which will wrap this module.

## What changes under you if you change nothing

Even with no edits, a test using the wrappers behaves differently once the
rework lands:

| Behaviour | Today | After |
| --- | --- | --- |
| Default image | `mongo:latest` from Docker Hub, requested as `linux/amd64` | `mongo:8`, multi-arch, overridable by `MONGOTEST_IMAGE` |
| Host port | Picked by mongotest, then bound by Docker, with a race between the two | Assigned by the daemon and read back |
| Dial address | Always `127.0.0.1` | Resolved from where the daemon is; see [Remote and nested daemons](remote-daemons.md) |
| Readiness | Five pings, 200 ms apart | `mongod` in the process list, then ping with backoff, up to 60 s |
| Replica set name | Always `cf`, whatever you passed | The name you passed |
| Scripts | Legacy `mongo` shell, missing on current images | `mongosh` |
| ++ctrl+c++ | Containers removed, process keeps running | Containers removed, process terminates |
| Finalizer | Removes the container if the GC runs | Gone; `Run` uses `t.Cleanup`, the reaper covers signals |
| Logging | logrus to stderr at info level | Discarded unless `WithLogger` |
| Dependencies | docker, easymongo, logrus, mongo-tools | mongo-driver and testify only |

The last row is the one most likely to be noticed: `go.sum` shrinks by a few
hundred lines, and `go mod tidy` will drop modules your project did not
otherwise use.

## Suggested order

1. **Now.** Where tests use easymongo methods through `conn`, switch them to
   `conn.MongoDriverClient()`. Pin an image in CI by tagging it under the
   name mongotest pulls, as shown in the [CI guide](../guides/ci.md#tips-for-stable-ci-runs).
2. **When PR #53 merges.** Nothing to do; the root package does not use
   `mongod` until #40.
3. **When #40 and #41 merge.** Run the suite. Replace
   `NewTestConnection(true)` plus cleanup with `Run(t)` at your leisure. Set
   `MONGOTEST_IMAGE` instead of tagging images.
4. **When #43 merges.** If you use the exporter, add the `exporter` module.
5. **When #42 merges.** If you are on driver v2, switch the import to
   `mongotest/v2`.
