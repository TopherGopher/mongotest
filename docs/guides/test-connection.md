---
title: Test connections
description: NewTestConnection, the TestConnection type, and using it with easymongo or the official driver.
status: changing
tracks: ["#40", "#41"]
---

# Test connections

`NewTestConnection` is the primary entry point. It returns a `*TestConnection`
that owns one container and one connection to the server inside it.

```go
func NewTestConnection(spinupDockerContainer bool) (*TestConnection, error)
```

## With a container

```go
conn, err := mongotest.NewTestConnection(true)
if err != nil {
    t.Fatal(err)
}
t.Cleanup(func() { _ = conn.KillMongoContainer() })
```

The connection has already answered a ping. Register the cleanup straight
after the error check, so a failing assertion still removes the container.

mongotest logs each start to stderr through logrus; that cannot be turned off
yet.

## Without a container

Pass `false` and no Docker call is made. mongotest connects to
`mongodb://127.0.0.1:27017` and expects a server to be listening there.

```go
conn, err := mongotest.NewTestConnection(false)
```

This is useful when:

- a CI job already runs MongoDB as a service container on the default port;
- you are iterating locally against a long-lived `mongod` and the second of
  container start-up per test is in the way;
- the machine has no Docker daemon at all.

`KillMongoContainer` is safe to call in this mode; with no container recorded
it returns `nil` immediately. Keep the `t.Cleanup` line in place so the test
works either way.

!!! warning "Shared server, shared data"
    Without a container there is no isolation between tests. Use a distinct
    database name per test and drop it in cleanup:

    ```go
    db := conn.D("test_" + t.Name())
    t.Cleanup(func() { _ = db.Drop() })
    ```

## What a TestConnection gives you

`TestConnection` embeds `*easymongo.Connection`, so every easymongo method is
promoted onto it. The ones you will reach for most:

| Call | Returns | Use |
| --- | --- | --- |
| `conn.D(name)` or `conn.Database(name)` | `*easymongo.Database` | Address a database. It is created on first write. |
| `db.C(name)` or `db.Collection(name)` | `*easymongo.Collection` | Address a collection. |
| `coll.Insert().One(v)` / `.Many(vs)` | ids, error | Insert documents. |
| `coll.Find(filter).One(&v)` / `.All(&vs)` | error | Query. |
| `coll.Count()` | int, error | Count documents. |
| `conn.MongoURI()` | string | The URI the connection was opened with. Hand it to code that builds its own client. |
| `conn.MongoDriverClient()` | `*mongo.Client` | The official driver client underneath. |
| `conn.Ping()` | error | Round-trip to the server. |

And the methods mongotest adds:

| Call | Use |
| --- | --- |
| `conn.MongoContainerID()` | The container ID, or `""` when no container was started. |
| `conn.KillMongoContainer()` | Force-remove the container and its volumes. Idempotent. |
| `conn.RunMongoScriptOnContainer(js)` | Run a JavaScript file inside the container. See [Scripts and exec](scripts-and-exec.md). |
| `conn.ExecCommandInMongoContainer(cmd)` | Run any command inside the container. |
| `conn.DatabaseExporter(db, coll)` | Build a JSON or CSV export. See [Exporting data](exporting-data.md). |

## Passing the server to code under test

Most application code does not take an easymongo connection; it takes a URI,
a `*mongo.Client` or a `*mongo.Database`. All three are one call away.

=== "URI"

    ```go
    repo, err := NewRepository(conn.MongoURI())
    ```

    Code that builds its own client from the URI must disconnect it before
    the container is removed; see [Order of cleanup](#order-of-cleanup).

=== "*mongo.Client"

    ```go
    repo := NewRepository(conn.MongoDriverClient())
    ```

    The client is owned by the `TestConnection`; do not call `Disconnect`
    on it yourself.

=== "*mongo.Database"

    ```go
    db := conn.MongoDriverClient().Database("app")
    svc := NewService(db)
    ```

## Order of cleanup

`t.Cleanup` functions run last-registered first. Register the container
removal first so it runs last:

```go
conn, err := mongotest.NewTestConnection(true)
require.NoError(t, err)
t.Cleanup(func() { _ = conn.KillMongoContainer() }) // runs last

client, err := mongo.Connect(ctx, options.Client().ApplyURI(conn.MongoURI()))
require.NoError(t, err)
t.Cleanup(func() { _ = client.Disconnect(ctx) })      // runs first
```

## Errors

| Error | Meaning |
| --- | --- |
| `mongotest.ErrFailedToConnectToDockerDaemon` | No daemon answered. See [Installation](../getting-started/installation.md#check-the-daemon). |
| `mongotest.ErrNoAvailablePorts` | The kernel would not assign a loopback port. |
| `mongotest.ErrMongoContainerAlreadyRunning` | An internal guard; a `TestConnection` tried to start a second container. |
| Anything else | A Docker or driver error. The container may still exist, so call `conn.KillMongoContainer()` before failing the test; it is safe on a connection returned with an error. |

Compare with `errors.Is`; the messages are not stable.
