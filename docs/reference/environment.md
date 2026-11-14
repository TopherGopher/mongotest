---
title: Environment variables
description: Every environment variable mongotest and its Docker clients read.
status: changing
tracks: ["PR #53"]
---

# Environment variables

mongotest itself reads no environment variables today. Its Docker clients
read the same ones the `docker` CLI does, so a machine where `docker version`
works is a machine where mongotest works.

## Read by the Docker clients

| Variable | Read by | Effect |
| --- | --- | --- |
| `DOCKER_HOST` | root package, `dockerapi`, `mobyclient` | Daemon address: `unix:///path`, `tcp://host:port`, `npipe://` (official client only). Overrides every other discovery step. |
| `DOCKER_CONTEXT` | `dockerapi`, `mobyclient` | The Docker context to use when `DOCKER_HOST` is unset. |
| `DOCKER_CONFIG` | `dockerapi`, `mobyclient` | Directory holding `config.json` and `contexts/`. Defaults to `~/.docker`. The `currentContext` key in `config.json` is honoured. |
| `DOCKER_TLS_VERIFY` | root package, `dockerapi`, `mobyclient` | When set to a non-empty value, verify the daemon's certificate using the CA under `DOCKER_CERT_PATH`. |
| `DOCKER_CERT_PATH` | root package, `dockerapi`, `mobyclient` | Directory with `ca.pem`, `cert.pem`, `key.pem` for a TLS-protected daemon. |
| `DOCKER_API_VERSION` | root package (legacy client), `mobyclient` | Pin the API version. `dockerapi` uses `WithAPIVersion` instead and negotiates by default. |
| `XDG_RUNTIME_DIR` | `dockerapi` | Where rootless Docker and Podman put their sockets. `/run/user/$UID` is used when unset. |
| `TEMP` | `dockerapi` on Windows | Where `podman machine` exposes its AF_UNIX socket. |

## Landing with `mongod`

[PR #53](https://github.com/TopherGopher/mongotest/pull/53) adds three
variables of mongotest's own. An option beats the variable, and the variable
beats the default. See [The mongod package](../changing/mongod.md#options).

| Variable | Sets | Default |
| --- | --- | --- |
| `MONGOTEST_IMAGE` | The image: a version, a repository, or a full reference such as `localhost:5000/team/mongo:8` | `mongo:8.0` |
| `MONGOTEST_START_TIMEOUT` | The start timeout, as a Go duration | `60s` |
| `MONGOTEST_HOST_IP` | The address containers are reachable at, when the detection is wrong | see [Remote and nested daemons](../changing/remote-daemons.md) |

## Not read

mongotest does not read `MONGO_URI`, `MONGODB_URI` or similar. When you call
`NewTestConnection(false)`, the address is fixed at `mongodb://127.0.0.1:27017`.
To point tests at another server, build your own client from the URI you
have, or wait for `Attach(ctx, uri)`, issue
[#40](https://github.com/TopherGopher/mongotest/issues/40).
