---
title: Getting started
description: What mongotest is, what it needs from your machine, and where to go first.
---

# Getting started

mongotest gives every Go test its own real MongoDB server. It starts an
official `mongo` image in a container on a free port, connects, hands you the
connection, and removes the container when the test is done. No shared
fixture, no leftover data between runs, no mocking the database.

<div class="grid cards" markdown>

-   :material-download:{ .lg .middle } **Installation**

    ---

    Add the module, confirm a container runtime is reachable, and run the
    first test.

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

!!! tip "No daemon? Use a local server instead"
    `NewTestConnection(false)` skips containers entirely and connects to a
    `mongod` already listening on `127.0.0.1:27017`. Everything else in the
    API works the same way. See [Test connections](../guides/test-connection.md#without-a-container).
