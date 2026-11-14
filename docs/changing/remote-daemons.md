---
title: Remote and nested daemons
description: How the address to dial is worked out when the Docker daemon is not on loopback, and what is still open.
status: landing
tracks: ["PR #53", "#52"]
---

# Remote and nested daemons

Today mongotest dials `127.0.0.1:<port>`, which only works when the test
process and the daemon share a network. In the setups below the container
starts but the connection fails.

## The four setups

| Setup | How it looks | Where the port really is |
| --- | --- | --- |
| **Socket bind-mount** (sibling containers) | Tests run in a container started with `-v /var/run/docker.sock:/var/run/docker.sock` | On the host's loopback, which the test container cannot reach |
| **True docker-in-docker** | A `docker:dind` service, `DOCKER_HOST=tcp://docker:2375` | On the dind container, reachable as `docker:<port>` |
| **Socket proxy** | `tecnativa/docker-socket-proxy` in front of the socket | Wherever the daemon behind the proxy puts it. The proxy may also refuse calls mongotest makes. |
| **Remote daemon** | `DOCKER_HOST=tcp://build-host:2376` | On `build-host` |

The socket bind-mount is the most common in CI. It is often called
docker-in-docker, though nothing is nested.

## How the address is worked out

`mongod.Start` works the address out before it creates anything, and uses
the first of these that applies. A process counts as containerised when
`/.dockerenv` or `/run/.containerenv` exists, which Docker and Podman create.

| # | Condition | Address |
| --- | --- | --- |
| 1 | `WithHostIP` was given | that value |
| 2 | `MONGOTEST_HOST_IP` is set | that value |
| 3 | The daemon is `tcp://`, `http://`, `https://` or `ssh://` | the daemon's hostname |
| 4 | Unix socket or named pipe, and this process is not containerised | `127.0.0.1` |
| 5 | Unix socket, this process is containerised but shares the host network (`--network host`, `hostNetwork` pods) | `127.0.0.1` |
| 6 | Unix socket, this process is containerised on a network of its own | the default route's gateway, from `/proc/net/route` |

Use the first two when the detection is wrong. `MONGOTEST_HOST_IP` is for CI
configuration that cannot pass an option.

When the address is loopback, the port is published on `127.0.0.1` only.
Otherwise it is published on every interface, and the container gets a root
user and a random password, which `URI()` includes; see
[Who can connect](mongod.md#who-can-connect).

## Using it

With `mongod.Start` these setups need no configuration, apart from a socket
proxy that refuses a call. If the address is wrong, set it:

```sh
MONGOTEST_HOST_IP=172.17.0.1 go test ./...
```

or in code, `mongod.WithHostIP("172.17.0.1")`.

## Still open

The rest of [#52](https://github.com/TopherGopher/mongotest/issues/52) is
open: a CI matrix for the four setups, and clearer errors behind a socket
proxy ([#72](https://github.com/TopherGopher/mongotest/issues/72)).

## Until it lands

The [CI guide](../guides/ci.md#the-shape-that-does-not-work-a-remote-daemon)
describes the setups to avoid with the current release: run the tests on the
same host as the daemon, or use a service container and
`NewTestConnection(false)`.
