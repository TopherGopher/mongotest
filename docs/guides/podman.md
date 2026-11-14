---
title: Podman
description: Running mongotest against Podman's Docker-compatible socket.
status: current
tracks: ["#51"]
---

# Podman

Podman serves the Docker Engine API, so mongotest works against it unchanged
once it can find the socket. Nothing in mongotest shells out to a `docker` or
`podman` binary.

## Start the socket

=== "Rootless (recommended)"

    ```sh
    systemctl --user enable --now podman.socket
    ls "$XDG_RUNTIME_DIR/podman/podman.sock"
    ```

=== "System-wide"

    ```sh
    sudo systemctl enable --now podman.socket
    ls /run/podman/podman.sock
    ```

=== "podman machine (macOS, Windows)"

    ```sh
    podman machine init
    podman machine start
    ```

    `podman machine` forwards the API to the Docker default socket on macOS,
    so no further configuration is usually needed. On Windows it exposes an
    AF_UNIX socket under `%TEMP%` since Podman 5.3, which the new client can
    dial.

## Point mongotest at it

Set `DOCKER_HOST` the same way you would for the `docker` CLI:

```sh
export DOCKER_HOST=unix://$XDG_RUNTIME_DIR/podman/podman.sock
go test ./...
```

With the new [`dockerapi`](../docker-clients/dockerapi.md) client, even this
is optional. When nothing is configured it probes, in order, rootless Docker,
system Docker, rootless Podman and system Podman, and uses the first socket
that answers. The root package will pick that behaviour up when it moves onto
`dockerapi`.

## API versions

The legacy client the root package uses today works with every supported
Podman, and so does `dockerapi`.

`dockerapi` also reports which engine answered:

```go
c, _ := dockerapi.FromEnv()
if c.Runtime() == dockerapi.RuntimePodman {
    // Podman identifies itself through the Libpod-API-Version header.
}
```

Version errors name the product, so a Podman user is never told to upgrade a
Docker daemon they do not have.

## Troubleshooting

??? failure "`could not connect to docker daemon`"
    The socket is not running or `DOCKER_HOST` points at the wrong path. Check
    with `podman --remote info` using the same `CONTAINER_HOST` value, or
    `curl --unix-socket "$XDG_RUNTIME_DIR/podman/podman.sock" http://d/_ping`.
