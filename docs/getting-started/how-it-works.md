---
title: How it works
description: What mongotest does between the call and the first query, and the defaults it chooses.
status: changing
tracks: ["PR #53", "#40"]
---

# How it works

A `TestConnection` corresponds one-to-one with a container. This page follows
one from creation to removal, and lists the defaults you may want to know
about before you depend on them.

## The lifecycle

<div class="mt-steps" markdown>

1.  **Find the daemon.** mongotest builds a Docker client from the
    environment: `DOCKER_HOST` if set, otherwise the platform default socket.
    It speaks the Engine API over HTTP; the `docker` binary is never
    executed. Failure here is `ErrFailedToConnectToDockerDaemon`.

2.  **Pick a port.** It asks the kernel for a free TCP port on `127.0.0.1` by
    listening on port `0`, reads the port that was assigned, and closes the
    listener. Failure is `ErrNoAvailablePorts`.

3.  **Create the container.** The container is named `mongo-<port>`, labelled
    `mongotest=regression`, and publishes its `27017/tcp` on
    `127.0.0.1:<port>`. The image is `registry.hub.docker.com/library/mongo:latest`.

4.  **Pull on demand.** If creation fails with *not found*, the image is not
    local. mongotest pulls it, drains the progress stream, then creates again.
    Later runs skip this step because the daemon caches the image.

5.  **Start and register.** The container is started, recorded in an
    in-process registry for the signal handler, and given a finalizer as a
    last resort.

6.  **Connect and wait.** A connection is opened to
    `mongodb://127.0.0.1:<port>/?directConnection=true` and pinged up to five
    times with a short pause in between. If `mongod` never answers, the
    container is removed and the error is returned.

7.  **Hand over.** The `*TestConnection` is returned. Everything after this is
    your test.

8.  **Remove.** `KillMongoContainer` force-removes the container together with
    its anonymous volumes and clears the registry entry. Calling it twice is
    harmless.

</div>

## Why a container per test

Sharing one server across tests looks cheaper, and it is, right up until two
tests disagree about what is in a collection. A container per test costs about
a second of start-up on a warm image cache and buys complete isolation: the
database is empty at the start of every test, parallel tests cannot see each
other, and a crash in one test cannot poison the next.

If a second is too much for a large table of cases, start one container for
the table and give each case its own database. The
[parallel tests guide](../guides/parallel-tests.md) shows both shapes.

## Defaults at a glance

| Setting | Default | Notes |
| --- | --- | --- |
| Image | `mongo:latest` from Docker Hub | Pinning an image is on the [roadmap](../project/roadmap.md). Pre-pull a tag you trust to control what `latest` resolves to. |
| Bind address | `127.0.0.1`, random free port | The server is never reachable from other machines. |
| Container name | `mongo-<port>` | Visible in `docker ps`. |
| Label | `mongotest=regression` | Used by the [cleanup commands](../guides/cleanup.md). |
| Connection URI | `mongodb://127.0.0.1:<port>/?directConnection=true` | `directConnection` keeps the driver from doing replica set discovery against a single node. |
| Ping retries | 5, with a 200 ms pause | Roughly one second of patience after the container starts. |
| Platform | `linux/amd64` | Requested explicitly when creating the container. |
| Teardown | Explicit `KillMongoContainer`, plus signal handler and finalizer | See [Cleanup and reaping](../guides/cleanup.md) for what each one covers. |

## What is not isolated

The container shares the host's Docker daemon, image cache and loopback
interface. Two `go test` processes on one machine will each get their own
containers on different ports and will not interfere. They will, however,
compete for CPU, so a heavy integration suite run with high `-p` or
`-parallel` values may see slower starts.

## Where the Docker client comes from

The root package currently uses the legacy `github.com/docker/docker` client.
The repository also contains a zero-dependency replacement,
[`dockerapi`](../docker-clients/dockerapi.md), behind a small interface,
[`dockerclient`](../docker-clients/index.md), and the root package is being
moved onto it. Nothing about the lifecycle above changes when that lands; the
dependency graph gets much smaller.
