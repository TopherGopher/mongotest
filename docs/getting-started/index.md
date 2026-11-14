---
title: Getting started
description: What mongotest is, what it needs from your machine, and where to go first.
status: current
---

# Getting started

mongotest gives a Go test its own MongoDB server. It starts the official
`mongo` image in a container on a free port, hands you a connection, and
removes the container when you call `KillMongoContainer`.

<div class="grid cards" markdown>

-   :material-download:{ .lg .middle } **Installation**

    ---

    Add the module, confirm a daemon is reachable, and run the first test.

    [:octicons-arrow-right-24: Install](installation.md)

-   :material-rocket-launch:{ .lg .middle } **Quickstart**

    ---

    A complete test file that starts a server, writes a document and reads it
    back, with every line explained.

    [:octicons-arrow-right-24: Quickstart](quickstart.md)

-   :material-cog-outline:{ .lg .middle } **How it works**

    ---

    What happens between `NewTestConnection(true)` and the first query, and
    why each step is there.

    [:octicons-arrow-right-24: How it works](how-it-works.md)

-   :material-book-open-page-variant:{ .lg .middle } **Guides**

    ---

    Usage patterns: parallel tests, replica sets, running scripts, exporting
    fixtures, cleanup and CI.

    [:octicons-arrow-right-24: Browse the guides](../guides/index.md)

</div>

## What you need

| Requirement | Why |
| --- | --- |
| **Go 1.27 or newer** | The module declares `go 1.27`. |
| **A Docker-compatible daemon** | Docker Engine, Docker Desktop or Podman. mongotest talks to the Engine API; it never shells out to the `docker` binary. |
| **Network access to Docker Hub on the first run** | The `mongo` image is pulled once and cached by the daemon. Later runs are offline. |
| **mongo-driver v1** | `MongoDriverClient()` returns a v1 `*mongo.Client`. v2 is issue [#42](https://github.com/TopherGopher/mongotest/issues/42). |
| **`mongo:5.0` for replica sets and script files** | Both use the `mongo` shell, which newer images do not ship. See [Replica sets](../guides/replica-sets.md#two-limits-today). |

!!! tip "No daemon? Use a local server instead"
    `NewTestConnection(false)` starts no container and connects to a `mongod`
    already listening on `127.0.0.1:27017`. Queries work the same way; scripts
    and exec need a container. See [Test connections](../guides/test-connection.md#without-a-container).
