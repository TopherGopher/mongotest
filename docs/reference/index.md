---
title: Reference
description: Where to find the generated API documentation, and a map of the exported surface.
---

# Reference

The authoritative API reference is generated from the source by pkg.go.dev.
This section is a map, not a copy: it tells you what exists and where it is
explained, and links out for signatures.

<div class="grid cards" markdown>

-   :fontawesome-brands-golang:{ .lg .middle } **[mongotest on pkg.go.dev](https://pkg.go.dev/github.com/tophergopher/mongotest)**

    ---

    The root package: `TestConnection`, the constructors, the one-shot
    helpers and the exporter.

-   :material-code-braces:{ .lg .middle } **[dockerclient](https://pkg.go.dev/github.com/tophergopher/mongotest/dockerclient)** · **[dockerapi](https://pkg.go.dev/github.com/tophergopher/mongotest/dockerapi)** · **[dockermock](https://pkg.go.dev/github.com/tophergopher/mongotest/dockermock)** · **[mobyclient](https://pkg.go.dev/github.com/tophergopher/mongotest/mobyclient)**

    ---

    The Docker client interface, its implementations and the test doubles.

</div>

## Root package surface

### Constructors

| Identifier | Explained in |
| --- | --- |
| `NewTestConnection(spinupDockerContainer bool) (*TestConnection, error)` | [Test connections](../guides/test-connection.md) |
| `NewReplicaSetContainer(rsName string) (*TestConnection, error)` | [Replica sets](../guides/replica-sets.md) |
| `EasyMongoWithContainer(f func(*easymongo.Connection) error) error` | [One-shot helpers](../guides/one-shot-helpers.md) |
| `MongoClientWithContainer(f func(*mongo.Client) error) error` | [One-shot helpers](../guides/one-shot-helpers.md) |

### `TestConnection` methods

`TestConnection` embeds `*easymongo.Connection`; the methods below are the
ones mongotest adds.

| Method | Explained in |
| --- | --- |
| `MongoContainerID() string` | [Test connections](../guides/test-connection.md#what-a-testconnection-gives-you) |
| `KillMongoContainer() error` | [Cleanup and reaping](../guides/cleanup.md) |
| `RunMongoScriptOnContainer(js string) (string, error)` | [Scripts and exec](../guides/scripts-and-exec.md) |
| `ExecCommandInMongoContainer(cmd []string) (string, error)` | [Scripts and exec](../guides/scripts-and-exec.md) |
| `DatabaseExporter(db, coll string) *DatabaseExporter` | [Exporting data](../guides/exporting-data.md) |

### `DatabaseExporter`

| Method | Explained in |
| --- | --- |
| `Query`, `Sort`, `Skip`, `Limit`, `Database`, `Collection`, `Filepath` | [Exporting data](../guides/exporting-data.md#builder-methods) |
| `ToJSONFile(prettify, jsonArray, relaxedJSON, gzip bool) (string, error)` | [Exporting data](../guides/exporting-data.md#terminal-methods) |
| `CSVFile() (string, error)` | [Exporting data](../guides/exporting-data.md#csvfile) |

### Functions

| Identifier | Explained in |
| --- | --- |
| `GetAvailablePort() (int, error)` | [How it works](../getting-started/how-it-works.md#the-lifecycle). Exposed because it is generally useful. |
| `ReapRunningContainers()` | [Cleanup and reaping](../guides/cleanup.md#2-the-signal-handler) |
| `GenerateCARoot() (*x509.Certificate, []byte, *rsa.PrivateKey)` | Generates a self-signed CA for the TLS mode under construction. |

### Errors

| Identifier | Explained in |
| --- | --- |
| `ErrFailedToConnectToDockerDaemon`, `ErrNoAvailablePorts`, `ErrMongoContainerAlreadyRunning` | [Test connections](../guides/test-connection.md#errors) |
| `ErrNotImplemented` | Returned by exporter methods that are declared but not yet written. |

## Docker client surface

| Package | Key identifiers | Explained in |
| --- | --- | --- |
| `dockerclient` | `Client`, `ContainerConfig`, `HostConfig`, `File`, `ExecResult`, `Logger`, `NopLogger`, the `Err*` sentinels | [Docker clients](../docker-clients/index.md), [Errors](../docker-clients/errors.md) |
| `dockerapi` | `New`, `FromEnv`, `With*` options, `Client.Negotiate`, `APIVersion`, `Runtime`, `ServerProduct` | [dockerapi](../docker-clients/dockerapi.md) |
| `mobyclient` | `New`, `FromEnv`, `WithHost`, `WithMobyClient`, `WithLogger` | [mobyclient](../docker-clients/mobyclient.md) |
| `dockermock` | `Mock`, `NewFake`, `NewDaemon`, `Daemon.Handle`, `Daemon.ServeDefaults`, `Daemon.ServeFake` | [dockermock](../docker-clients/dockermock.md) |
| `dockerclienttest` | `Conformance`, `Benchmarks` | [Conformance suite](../docker-clients/conformance.md) |

## Also in this section

- [Environment variables](environment.md) that mongotest and its Docker
  clients read.
- [Writing a new page](writing-docs.md) for this site.
