---
title: Guides
description: Usage patterns for mongotest, from a single test to a CI pipeline.
status: current
---

# Guides

Each guide covers one way of using mongotest, with a complete example. Read
them in any order.

<div class="grid cards" markdown>

-   :material-connection:{ .lg .middle } **[Test connections](test-connection.md)**

    ---

    `NewTestConnection`, what a `TestConnection` gives you, and how to use it
    with easymongo or the official driver.

-   :material-flash:{ .lg .middle } **[One-shot helpers](one-shot-helpers.md)**

    ---

    `EasyMongoWithContainer` and `MongoClientWithContainer`: a container for
    the length of one function call.

-   :material-table-multiple:{ .lg .middle } **[Table-driven and parallel tests](parallel-tests.md)**

    ---

    One container per case or one per table; `t.Parallel` and
    `go test -p` without collisions.

-   :material-source-branch:{ .lg .middle } **[Replica sets](replica-sets.md)**

    ---

    A single-member replica set for transactions and change streams, and the
    current limits of the implementation.

-   :material-console:{ .lg .middle } **[Scripts and exec](scripts-and-exec.md)**

    ---

    Running a shell script or any command inside the container.

-   :material-export:{ .lg .middle } **[Exporting data](exporting-data.md)**

    ---

    Turning a populated test database into JSON or CSV fixtures.

-   :material-broom:{ .lg .middle } **[Cleanup and reaping](cleanup.md)**

    ---

    What removes containers, in what order, and how to remove stray ones.

-   :material-github:{ .lg .middle } **[Continuous integration](ci.md)**

    ---

    Running the suite under GitHub Actions and other hosted runners.

-   :simple-podman:{ .lg .middle } **[Podman](podman.md)**

    ---

    Using Podman's Docker-compatible socket, rootless and with
    `podman machine`.

</div>
