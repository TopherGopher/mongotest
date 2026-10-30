---
title: The mongod package
description: "The driver-free container lifecycle landing in PR #53: Start, Options, images, readiness, address resolution and the reaper."
status: landing
tracks: ["PR #53", "#32", "#33"]
---

# The mongod package

`github.com/tophergopher/mongotest/mongod` starts a MongoDB container, waits
until it is genuinely usable, tells you where to connect, and makes sure it
does not outlive the process that started it. It imports no MongoDB driver,
so the driver v1 root module and the `v2/` module can both share it, and it
takes a [`dockerclient.Client`](../docker-clients/index.md) rather than a
concrete client, so a test can hand it a double.

Most tests will not call it directly; they will use
[`mongotest.Run(t)`](next-api.md), which wraps it. Read this page when you
need the container without a driver, or want to know what the options mean.

```go
c, err := mongod.Start(ctx)
if err != nil {
    return err
}
defer c.Stop(context.Background())

client, err := mongo.Connect(options.Client().ApplyURI(c.URI()))
```

With no options that runs `mongo:8`, publishes 27017 on a host port the
daemon picks, labels the container `mongotest=regression`, and waits up to a
minute. Everything in that sentence is an option, an environment variable, or
both.

## Configuring it

`Options` is the one place a container is configured: public fields, a
chainable `With*` method for each, and package-level `With*` functions that
return a fresh `*Options` so a chain can start anywhere. `Start` takes any
number of them and merges left to right without mutating any of them.

```go
mongod.Start(ctx, mongod.WithImage("8.0").WithReplicaSet("rs0"))       // chained
mongod.Start(ctx, mongod.NewOptions().WithDocker(c).WithPort(27018))   // explicit
mongod.Start(ctx, &mongod.Options{Image: mongod.ImageAtlasLocal})      // literal
mongod.Start(ctx, base, mongod.WithReplicaSet("rs0"))                  // merged; base untouched
```

The `With` methods mutate their receiver, which is what makes a chain one
expression. `Options.Clone()` specialises a shared base without changing it.

Every setting comes from the first of three places that has it: **the option,
then the environment variable, then the default**.

| Setting | Option | Environment | Default |
| --- | --- | --- | --- |
| image | `WithImage`, `WithMongoImage` | `MONGOTEST_IMAGE` | `mongo:8` |
| registry | `WithRegistry` | `MONGOTEST_IMAGE_REGISTRY` | Docker Hub |
| repository | `WithRepository` | `MONGOTEST_IMAGE_REPOSITORY` | `mongo` |
| version | `WithVersion` | `MONGOTEST_IMAGE_VERSION` | `8` |
| host port | `WithPort` | `MONGOTEST_PORT` | the daemon chooses |
| readiness budget | `WithStartTimeout` | `MONGOTEST_START_TIMEOUT` | 60s |
| dial address | `WithHostIP` | `MONGOTEST_HOST_IP` | resolved; see below |
| replica set | `WithReplicaSet` | | none |
| extra `mongod` flags | `WithMongodArgs` | | none |
| labels | `WithLabel` | | always includes `mongotest=regression` |
| container name | `WithName` | | `mongotest-` plus eight random hex characters |
| Docker client | `WithDocker` | | `dockerapi.FromEnv()` |
| logger | `WithLogger` | | discard |
| TLS | `WithTLS` | | off; see [TLS](#tls) |

Inference and validation happen once, in `Options.Resolve`, which `Start`
calls and which is exported so you can see what a configuration means without
starting anything. No helper returns an error: a bad value is kept and
reported from `Resolve`, so a chain stays one expression and nothing is
created on a configuration that cannot work.

!!! tip "Pin the image in CI with one variable"
    `MONGOTEST_IMAGE=mongo:8.0.12` in the job environment pins every container
    a test run starts, with no code change. An explicit `WithImage` in a test
    still wins, which is what you want for the one test that checks an
    upgrade path.

## Which image

`MongoImage` holds an image as `{Registry, Repository, Version}`, so one part
changes without the others being restated, which is what a CI job moving off
Docker Hub actually needs.

```go
mongod.NewMongoImage("public.ecr.aws", "docker/library/mongo", "8")  // parts
mongod.MustParseMongoImage("public.ecr.aws/docker/library/mongo:8")  // a constant string
mongod.ParseMongoImage(os.Getenv("IMAGE"))                           // a string that might be wrong
```

`WithImage` takes the same strings and sets only the parts the reference
names: a version alone (`8.0`), a repository alone (`mongo`), both
(`mongo:8.0`), a full registry path with a tag, a registry with a port
(`localhost:5000/team/mongo:8`), or a digest.

Five images are named, and the differences between them are encoded rather
than left to be discovered:

| Constant | Reference | Bare version tags | `mongod` flags via `Cmd` | Replica set |
| --- | --- | --- | --- | --- |
| `ImageDockerHub` | `mongo:8` | yes | yes | none |
| `ImagePublicECR` | `public.ecr.aws/docker/library/mongo:8` | yes | yes | none |
| `ImageCommunity` | `mongodb/mongodb-community-server:8.0-ubi9` | **no** | yes | none |
| `ImageEnterprise` | `mongodb/mongodb-enterprise-server:8.0-ubi9` | **no** | yes | none |
| `ImageAtlasLocal` | `mongodb/mongodb-atlas-local:8` | yes | **no** | pre-initiated |

Two combinations are refused at `Resolve` because both otherwise fail in a
way that does not name the option responsible:

- MongoDB's own server images publish no bare version tags: `:8` does not
  exist there, only `8.0-ubi9` and the like. A bare version is
  `ErrNoSuchVersion`, not a pull that 404s.
- Atlas Local has no `ENTRYPOINT`, so `mongod` flags would replace its
  program. `WithReplicaSet` and `WithMongodArgs` are `ErrUnsupportedForImage`
  for it, and the message says it already runs a replica set of its own.

An image the package does not know is assumed to behave like the official
one, which is the right default for a private mirror.

### Atlas Local restarts itself

`mongodb/mongodb-atlas-local` brings `mongod` up, initiates a replica set,
then stops that process and starts a different one a few seconds later.
`Start` returns on the first one. A client that connects the instant `Start`
returns can see one disconnection shortly afterwards, which looks like a
flake. The driver layers' ping-with-backoff will tolerate it
([#54](https://github.com/TopherGopher/mongotest/issues/54)); until then,
retry the first operation. `MongoImage.ReplicaSetPreconfigured()` reports
which images behave this way, and `Container.ReplicaSet()` is empty for them
because the name is generated inside the container.

## The container

| Method | Returns |
| --- | --- |
| `ID()`, `Name()`, `Image()` | What was started. |
| `Port()` | The published host port, read back from the daemon after start. |
| `Host()` | The resolved dial address. Not assumed to be loopback; see [Remote and nested daemons](remote-daemons.md). |
| `URI()` | `mongodb://<host>:<port>/?directConnection=true`, plus `&tls=true` in TLS mode. |
| `ReplicaSet()` | The name given to `WithReplicaSet`, or empty. |
| `Stop(ctx)` | Force-remove with volumes, unregister from the reaper. Idempotent and nil-safe. |

## Readiness

`Start` returns when three things are true: the container is running,
`mongod` itself appears in the container's process list, and the resolved
address accepts a TCP connection. The middle one matters: Docker's userland
proxy binds the published port as soon as the container is *created*, so a
dial succeeds against a container whose only process is `runc init`. Polling
the process list is what makes the probe honest.

What it deliberately does **not** guarantee is that `mongod` is accepting
MongoDB connections, because the proxy accepts on its behalf. The driver
layers ping with backoff for that, bounded by the same `WithStartTimeout`.

## Ports

The create request publishes 27017 with an empty host port and `Start` reads
back what the daemon chose. Finding a free port and binding it are two steps,
and parallel tests lose the race between them regularly. `WithPort` and
`GetAvailablePort` remain for callers who need a port known in advance.

## Teardown and the reaper

The `reaper` package replaces the root package's signal handler. Every
started container is registered, and `Stop` gives the registration back, so
a run interrupted between the two still cleans up: on `SIGINT` or `SIGTERM`
the containers are removed and the process then dies of the signal it was
sent.

| Old root handler | Now |
| --- | --- |
| Installed from `init()` in every importing binary | Installed lazily by the first `Start` |
| Listened for `SIGKILL`, which cannot be caught | `SIGINT` and `SIGTERM` only |
| Handled one signal, then the goroutine returned | Removes itself first, so a second ++ctrl+c++ kills at once |
| Never re-raised, so ++ctrl+c++ stopped terminating the process | `signal.Reset`, then re-raise |

A signal is not the only way a run ends, so the same teardown is a plain
call, which is what a `TestMain` should defer:

```go
func TestMain(m *testing.M) {
    code := m.Run()
    _ = mongod.ReapRunningContainers(context.Background())
    os.Exit(code)
}
```

Teardowns register as `func(context.Context) error`, not as containers, so
the dependency points one way: `mongod` imports `reaper`, and `reaper` knows
nothing about containers. The driver layers and the exporter can register
with the same handler.

## TLS

`WithTLS()` lands in PR #53 as a flag that puts `tls=true` in the URI. The
certificate material is
[#34](https://github.com/TopherGopher/mongotest/issues/34): an ephemeral CA
and a server certificate with SANs for `127.0.0.1` and `localhost`, generated
in memory, copied into the created-but-not-started container with the archive
endpoint (never a bind mount), and `mongod` started with
`--tlsMode requireTLS`. `Container.TLSConfig()` and `Container.CAPEM()` are
exposed for the driver layers and for your own clients.

## Exec and scripts

Not on `Container` in PR #53; go through the `dockerclient.Client` you
supplied. [#35](https://github.com/TopherGopher/mongotest/issues/35) adds
`Container.Exec(ctx, cmd...)` and `Container.RunScript(ctx, js)`, the latter
running `mongosh --quiet --norc --eval <js>` inside the container, with the
TLS flags added automatically in TLS mode. This is what fixes
[today's `RunMongoScriptOnContainer`](../guides/scripts-and-exec.md), which
shells out to a `mongo` binary that current images no longer ship.

## Persistent data on a named volume

[#55](https://github.com/TopherGopher/mongotest/issues/55) proposes
`WithVolume(name)`: put the data directory on a named volume so that data
survives the container and a later run on the same volume sees it.

```go
// run 1
c, _ := mongod.Start(ctx, mongod.WithVolume("cart-tests"))
// ... writes ... c.Stop(ctx)   -> container gone, volume kept

// run 2: same data
c, _ = mongod.Start(ctx, mongod.WithVolume("cart-tests"))
```

The groundwork is measured: a named volume is created by `ContainerCreate`
with no extra client call, and survives the `rm -v` that `Stop` already does.
Unset keeps today's behaviour, an anonymous volume removed with the
container. Two caveats are recorded on the issue: `Stop` will never remove a
named volume, and a volume written by a newer `mongod` refuses an older one
with a feature-compatibility error that does not point at the volume.

## Unit testing code that starts containers

Wire a double in with `WithDocker` and the whole lifecycle runs in memory:

```go
docker := dockermock.NewFake(dockermock.WithImages("mongo:8"))
c, err := mongod.Start(ctx, mongod.WithDocker(docker))
require.NoError(t, err, "a fake daemon with the image present must start a container")
defer c.Stop(ctx)

created := docker.Containers()
require.Len(t, created, 1, "exactly one container must have been created")
```

`Mock` is the right double when the test is about which calls were made, for
instance that a missing image triggers exactly one pull. See
[dockermock](../docker-clients/dockermock.md).
