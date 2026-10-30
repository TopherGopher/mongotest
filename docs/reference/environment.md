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

[PR #53](https://github.com/TopherGopher/mongotest/pull/53) adds variables of
mongotest's own. Each is read only when the matching option is not given, so
the order is option, then environment, then default. See
[The mongod package](../changing/mongod.md#configuring-it).

| Variable | Overrides | Default |
| --- | --- | --- |
| `MONGOTEST_IMAGE` | The whole image reference, or any part of it: `8.0`, `mongo:8.0`, `localhost:5000/team/mongo:8` | `mongo:8` |
| `MONGOTEST_IMAGE_REGISTRY` | The registry alone | Docker Hub |
| `MONGOTEST_IMAGE_REPOSITORY` | The repository alone | `mongo` |
| `MONGOTEST_IMAGE_VERSION` | The tag alone | `8` |
| `MONGOTEST_PORT` | The published host port | the daemon chooses |
| `MONGOTEST_START_TIMEOUT` | The readiness budget, as a Go duration | `60s` |
| `MONGOTEST_HOST_IP` | The address to dial, when detection gets it wrong | resolved; see [Remote and nested daemons](../changing/remote-daemons.md) |

The `mongod` test binary clears all of these in its `TestMain`, so a runner
that pins them cannot break the package's own assertions. Your tests may want
to do the same for the one test that checks a specific image.

## Not read

mongotest does not read `MONGO_URI`, `MONGODB_URI` or similar. When you call
`NewTestConnection(false)`, the address is fixed at `mongodb://127.0.0.1:27017`.
To point tests at another server, build your own client from the URI you
have, or wait for `Attach(ctx, uri)` on the roadmap.
