---
title: Installation
description: Add mongotest to a Go module and verify a container runtime is reachable.
---

# Installation

## Add the module

```sh
go get github.com/tophergopher/mongotest
```

mongotest is a test helper, so it normally appears only in `_test.go` files.
Go tracks it in `go.mod` like any other dependency.

## Check the container runtime

mongotest connects to the Docker Engine API the same way the `docker` command
line does: through `DOCKER_HOST` if it is set, otherwise through the default
socket for your platform. The simplest check is the one you already know:

```sh
docker version
```

If that prints a server version, mongotest will find the same daemon.

=== "Docker Engine (Linux)"

    Install from [docs.docker.com](https://docs.docker.com/engine/install/)
    and make sure your user can reach the socket without `sudo`:

    ```sh
    sudo usermod -aG docker "$USER"
    newgrp docker
    docker version
    ```

=== "Docker Desktop (macOS, Windows)"

    Start Docker Desktop and wait for the whale to settle. The default socket
    is exposed to the host automatically. On Windows, mongotest cannot dial
    the named pipe directly; enable **Expose daemon on tcp://localhost:2375**
    in Docker Desktop settings, or set `DOCKER_HOST` to that address.

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

The first `NewTestConnection(true)` on a machine pulls `mongo:latest`, which
can take a minute on a slow connection and will make that one test look slow.
Pull ahead of time to keep the first run quiet:

```sh
docker pull mongo
```

## Run a smoke test

Create `smoke_test.go` anywhere in your module and run it:

```go title="smoke_test.go"
package yours

import (
    "testing"

    "github.com/tophergopher/mongotest"
)

func TestMongoIsReachable(t *testing.T) {
    conn, err := mongotest.NewTestConnection(true)
    if err != nil {
        t.Fatalf("start mongo: %v", err)
    }
    t.Cleanup(func() { _ = conn.KillMongoContainer() })

    if err := conn.Ping(); err != nil {
        t.Fatalf("ping: %v", err)
    }
    t.Logf("mongo is up in container %s", conn.MongoContainerID())
}
```

```sh
go test -run TestMongoIsReachable -v ./...
```

You should see a log line with a container ID and the container should be gone
from `docker ps -a` when the test finishes.

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
    The kernel refused to hand out a loopback port, which almost always means
    the process is out of file descriptors. Raise `ulimit -n` or look for a
    test that leaks connections.

??? failure "The test hangs for a minute, then fails to ping"
    The container started but `mongod` never answered. Check the container
    logs with `docker logs $(docker ps -aq --filter label=mongotest=regression | head -1)`.
    A common cause is an architecture mismatch: mongotest requests a
    `linux/amd64` image, and some ARM hosts have emulation disabled.
