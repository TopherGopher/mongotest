---
title: How it works
description: What mongotest does between the call and the first query, and the defaults it chooses.
status: changing
tracks: ["PR #53", "#40"]
---

# How it works

Each `TestConnection` owns one container. This page follows one from creation
to removal and lists the defaults.

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
    local. mongotest pulls it and creates again.
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
    its anonymous volumes. Calling it twice is harmless.

</div>

## Why a container per test

Each test starts with an empty database, so tests cannot see each other's
data and can run in parallel. If one container per test is too slow for a
large table of cases, share one and give each case its own database; see
[Table-driven and parallel tests](../guides/parallel-tests.md).

## Defaults at a glance

| Setting | Default | Notes |
| --- | --- | --- |
| Image | `mongo:latest` from Docker Hub | To pin a version today, tag it under this name; see [Tips for stable CI runs](../guides/ci.md#tips-for-stable-ci-runs). |
| Bind address | `127.0.0.1`, random free port | The server is never reachable from other machines. |
| Container name | `mongo-<port>` | Visible in `docker ps`. |
| Label | `mongotest=regression` | Used by the [cleanup commands](../guides/cleanup.md). |
| Connection URI | `mongodb://127.0.0.1:<port>/?directConnection=true` | `directConnection` keeps the driver from doing replica set discovery against a single node. |
| Ping retries | 5, with a 200 ms pause | |
| Platform | `linux/amd64` | Requested explicitly when creating the container. |
| Teardown | Explicit `KillMongoContainer`, plus signal handler and finalizer | See [Cleanup and reaping](../guides/cleanup.md) for what each one covers. |

## The Docker client

The root package uses the `github.com/docker/docker` client today and is
moving to [`dockerapi`](../docker-clients/dockerapi.md).
