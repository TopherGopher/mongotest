---
title: Remote and nested daemons
description: How the address to dial is resolved when the Docker daemon is not on the loopback, and what is still open under #52.
status: landing
tracks: ["PR #53", "#52"]
---

# Remote and nested daemons

Everything in mongotest today assumes a published container port is
reachable at `127.0.0.1:<port>` from the test process. That is true when the
test process and the daemon share a network namespace, which is the developer
laptop, and not much else. In every shape below the container starts, the
daemon reports a port, and the connection times out with nothing to explain
why.

## The four shapes

| Shape | How it looks | Where the port really is |
| --- | --- | --- |
| **Socket bind-mount** (sibling containers) | Tests run in a container started with `-v /var/run/docker.sock:/var/run/docker.sock` | On the host's loopback, a different namespace from the test container. The most common CI shape, and the one usually meant by "docker-in-docker" though nothing is nested. |
| **True docker-in-docker** | A `docker:dind` service, `DOCKER_HOST=tcp://docker:2375` | On the dind container, reachable as `docker:<port>` |
| **Socket proxy** | `tecnativa/docker-socket-proxy` in front of the socket | As one of the above, plus a second failure mode: endpoints the proxy denies |
| **Remote daemon** | `DOCKER_HOST=tcp://build-host:2376` | On `build-host` |

## How the address is resolved

PR #53 replaces the constant with a resolution that happens once in
`mongod.Start`, before anything is created, most specific first:

| # | Condition | Address |
| --- | --- | --- |
| 1 | `WithHostIP` was given | that value |
| 2 | `MONGOTEST_HOST_IP` is set | that value |
| 3 | The daemon is `tcp://`, `http://`, `https://` or `ssh://` | the daemon's hostname |
| 4 | Unix socket or named pipe, and this process is not containerised | `127.0.0.1` |
| 5 | Unix socket, this process is containerised but shares the host network (`--network host`, `hostNetwork` pods) | `127.0.0.1` |
| 6 | Unix socket, this process is containerised on a network of its own | the default route's gateway, from `/proc/net/route` |

The first two are the escape hatch, and they must exist: no detection covers
every topology. `MONGOTEST_HOST_IP` is for CI configuration that cannot pass
an option.

**The bind follows the resolution.** Docker takes the publish address
literally: a port published on `127.0.0.1` is refused at the bridge gateway
from a sibling container, while the same port on `0.0.0.0` is reachable. So
when the resolved address is loopback the container publishes on `127.0.0.1`,
keeping the laptop case off the machine's other interfaces, and in every
other case it publishes on all interfaces.

Containerisation is detected from the mount points of the network files a
runtime binds in, not from the presence of Docker paths in the mount table; a
host merely running Docker would otherwise be sent to the bridge gateway.

## Using it

Nothing, in the common case: the shapes above work without configuration
once the root package is on `mongod`. When detection gets it wrong:

```sh
MONGOTEST_HOST_IP=172.17.0.1 go test ./...
```

or in code, `mongod.WithHostIP("172.17.0.1")`.

## Still open under #52

PR #53 resolves the address. The rest of
[#52](https://github.com/TopherGopher/mongotest/issues/52) is not yet done:

- **A CI matrix** that runs the whole lifecycle in all four shapes, from
  committed compose files, rather than only on a laptop.
- **Socket proxy error quality.** A denied endpoint should produce an error
  naming the endpoint and the proxy setting to enable, rather than an
  HTML page from HAProxy. For `tecnativa/docker-socket-proxy` the settings
  this client needs are `VERSION`, `IMAGES`, `CONTAINERS`, `POST`, `EXEC`,
  `ALLOW_START`, `ALLOW_STOP` and `ALLOW_RESTARTS`.
- **Exec through a proxy.** Exec hijacks the connection with an HTTP 101
  upgrade. A proxy that does not pass upgrades through breaks exec while
  everything else works; the error should say so.
- **Buffered pull streams** through a proxy, which can trip a client timeout
  on a pull that is progressing.
- **The `Libpod-API-Version` header** may be stripped by a proxy; Podman
  detection must fall back to the component list.
- **The shared-network strategy** for sibling containers: put the mongo
  container on a network the test container is already on and dial its
  container port directly. Needs network attachment on the client interface,
  so it waits on the interface, both implementations and the conformance
  suite.

## Until it lands

The [CI guide](../guides/ci.md#the-shape-that-does-not-work-a-remote-daemon)
describes the shapes to avoid with the current release: run the tests on the
same host as the daemon, or use a service container and
`NewTestConnection(false)`.
