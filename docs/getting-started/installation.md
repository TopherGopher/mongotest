---
title: Installation
description: Add mongotest to a Go module and check that a Docker daemon is reachable.
status: current
---

# Installation

## Add the module

```sh
go get github.com/tophergopher/mongotest
```

## Check the daemon

mongotest finds the daemon through `DOCKER_HOST` if it is set, and otherwise
at the default socket. Check that a daemon is running:

```sh
docker version
```

If that prints a server version, a daemon is running. If your `docker` CLI
uses a context, set `DOCKER_HOST` as well; see
[Troubleshooting](#troubleshooting-the-first-run).

=== "Docker Engine (Linux)"

    Install from [docs.docker.com](https://docs.docker.com/engine/install/)
    and make sure your user can reach the socket without `sudo`:

    ```sh
    sudo usermod -aG docker "$USER"
    newgrp docker
    docker version
    ```

=== "Docker Desktop (macOS, Windows)"

    Start Docker Desktop. mongotest finds its socket, or its named pipe on
    Windows, without configuration.

=== "Podman"

    Podman serves the same API. Start the socket and either point
    `DOCKER_HOST` at it or let discovery find it:

    ```sh
    systemctl --user enable --now podman.socket
    export DOCKER_HOST=unix://$XDG_RUNTIME_DIR/podman/podman.sock
    ```

    The [Podman guide](../guides/podman.md) covers rootless setups,
    `podman machine`, and the API version differences.

## Pull the image once

The first `NewTestConnection(true)` on a machine pulls `mongo:latest`. To keep
that download out of a test run, pull it first:

```sh
docker pull mongo
```

## Check the installation

Run the [quickstart](quickstart.md) test.

## Troubleshooting the first run

??? failure "`could not connect to docker daemon`"
    The daemon is not running, or it is listening somewhere mongotest did not
    look. Run `docker version`; if that fails too, start the daemon. If it
    works, your CLI is using a context mongotest cannot see. Set `DOCKER_HOST`
    explicitly:

    ```sh
    export DOCKER_HOST=$(docker context inspect --format '{{.Endpoints.docker.Host}}')
    ```

??? failure "`no ports are available to bind the docker mongo instance to`"
    The kernel refused to assign a loopback port, usually because the process
    has run out of file descriptors. Raise `ulimit -n`, or look for a test
    that leaks connections.

??? failure "The container starts, but the test fails to ping"
    The container started but `mongod` never answered. Check the container
    logs with `docker logs $(docker ps -aq --filter label=mongotest=regression | head -1)`.
    A common cause is an architecture mismatch: mongotest requests a
    `linux/amd64` image, and some ARM hosts have emulation disabled.
