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

Podman capped its Docker-compatible API at 1.41 from 4.x through 5.7 and
raised it to 1.44 in 5.8. Some Docker clients refuse to negotiate below their
own preferred version and cannot talk to those releases at all.

- The legacy client the root package uses today speaks API 1.41, which every
  supported Podman accepts.
- `dockerapi` asks for 1.44 and accepts anything down to 1.24, negotiating
  downwards, so it works against all of them. A test in the repository fails
  if that floor is ever raised above Podman's cap.

`dockerapi` also reports which engine answered:

```go
c, _ := dockerapi.FromEnv()
if c.Runtime() == dockerapi.RuntimePodman {
    // Podman identifies itself through the Libpod-API-Version header.
}
```

Version errors name the product, so a Podman user is never told to upgrade a
Docker daemon they do not have.

## Differences you may notice

| Behaviour | Docker | Podman |
| --- | --- | --- |
| Image reference `registry.hub.docker.com/library/mongo:latest` | Pulled from Docker Hub | Pulled from Docker Hub. Podman's short-name aliases are not involved because the reference is fully qualified. |
| Port publishing on `127.0.0.1` | Loopback on the host | Loopback on the host (rootless uses a user-mode network stack; slightly slower first connection) |
| `docker ps` equivalents in the [cleanup guide](cleanup.md) | `docker ...` | `podman ...` with identical flags |
| Memory per container | Daemon-managed | No daemon; each container is a process tree under your user |

## Troubleshooting

??? failure "`could not connect to docker daemon`"
    The socket is not running or `DOCKER_HOST` points at the wrong path. Check
    with `podman --remote info` using the same `CONTAINER_HOST` value, or
    `curl --unix-socket "$XDG_RUNTIME_DIR/podman/podman.sock" http://d/_ping`.

??? failure "Container starts, ping never succeeds, on a rootless setup"
    Rootless networking can take a moment longer to bring up the port
    forward. If it is consistently too slow, enable `pasta` or `slirp4netns`
    port-forwarding tuning per the Podman docs, or run the socket as root.
