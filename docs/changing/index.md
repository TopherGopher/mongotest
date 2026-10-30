---
title: What's changing
description: The refactor in progress, what each status badge means, and where every piece stands.
status: current
tracks: ["#48"]
---

# What's changing

mongotest is being rebuilt from the Docker layer up. The calls documented in
the guides keep working throughout, because every old name is kept as a
plain wrapper over the new API. What changes is what is underneath them, and
what new code should reach for.

This section documents the new pieces **before they land**, from the open
pull requests and issues, so the shape can be read, discussed and planned
against. Every page on the site says which of these it is:

<div class="mt-legend" markdown>
<div><span class="mt-status__badge current">Current</span><span>Describes what is on <code>master</code> today.</span></div>
<div><span class="mt-status__badge changing">Changing</span><span>Works today, but the API on the page is being reworked. The old calls stay as wrappers.</span></div>
<div><span class="mt-status__badge landing">Landing</span><span>Implemented in an open pull request. Not on <code>master</code> yet; details may still move.</span></div>
<div><span class="mt-status__badge planned">Planned</span><span>Specified in an issue, not yet implemented.</span></div>
</div>

The same badge appears as an icon next to the page in the navigation.

## Where things stand

| Piece | Status | Where | Documented |
| --- | --- | --- | --- |
| `dockerclient`, `dockerapi`, `dockermock`, conformance suite | on master | [#48](https://github.com/TopherGopher/mongotest/issues/48) | [Docker clients](../docker-clients/index.md) |
| `mobyclient` | on master | [#48](https://github.com/TopherGopher/mongotest/issues/48) | [mobyclient](../docker-clients/mobyclient.md) |
| `mongod`: lifecycle, options, images, readiness, address resolution | **landing** | [PR #53](https://github.com/TopherGopher/mongotest/pull/53), closes [#32](https://github.com/TopherGopher/mongotest/issues/32) | [The mongod package](mongod.md) |
| `reaper`: registry and signal handling that re-raises | **landing** | [PR #53](https://github.com/TopherGopher/mongotest/pull/53), closes [#33](https://github.com/TopherGopher/mongotest/issues/33) | [The mongod package](mongod.md#teardown-and-the-reaper) |
| TLS with a real server certificate | planned | [#34](https://github.com/TopherGopher/mongotest/issues/34) | [The mongod package](mongod.md#tls) |
| `Exec` and `RunScript` through `mongosh` | planned | [#35](https://github.com/TopherGopher/mongotest/issues/35) | [The mongod package](mongod.md#exec-and-scripts) |
| Logging behind a `Logger` interface, adapters for logrus, zap, zerolog | planned | [#36](https://github.com/TopherGopher/mongotest/issues/36) to [#39](https://github.com/TopherGopher/mongotest/issues/39) | [Start, Run and Attach](next-api.md#logging) |
| Root package: `Start`, `Run`, `Attach`, `Instance`, replica set initiation | planned | [#40](https://github.com/TopherGopher/mongotest/issues/40) | [Start, Run and Attach](next-api.md) |
| Root package: today's names as wrappers | planned | [#41](https://github.com/TopherGopher/mongotest/issues/41) | [Migrating](migration.md) |
| `v2/` module on driver v2 | planned | [#42](https://github.com/TopherGopher/mongotest/issues/42) | [Start, Run and Attach](next-api.md#driver-v2) |
| `exporter/` module | planned | [#43](https://github.com/TopherGopher/mongotest/issues/43) | [Exporting data](../guides/exporting-data.md) |
| Named volumes for persistent data | planned | [#55](https://github.com/TopherGopher/mongotest/issues/55) | [The mongod package](mongod.md#persistent-data-on-a-named-volume) |
| Docker-in-docker, socket proxies, remote daemons | partly landing | [#52](https://github.com/TopherGopher/mongotest/issues/52), [PR #53](https://github.com/TopherGopher/mongotest/pull/53) | [Remote and nested daemons](remote-daemons.md) |
| Remove the old implementation, workspace, CI, releases | planned, last | [#44](https://github.com/TopherGopher/mongotest/issues/44) to [#47](https://github.com/TopherGopher/mongotest/issues/47) | [Roadmap](../project/roadmap.md) |

The [roadmap](../project/roadmap.md) has the order and the reasoning. The
tracking issue [#48](https://github.com/TopherGopher/mongotest/issues/48) has
the live checklist.

## What stays the same

Agreed with the maintainer and written into
[#41](https://github.com/TopherGopher/mongotest/issues/41): the old names are
kept as convenience wrappers, documented as such, **not** marked deprecated.
`NewTestConnection`, `NewReplicaSetContainer`, `MongoClientWithContainer`,
`TestConnection`, `KillMongoContainer`, `MongoContainerID`,
`RunMongoScriptOnContainer`, `ExecCommandInMongoContainer` and
`GetAvailablePort` all survive. The one exception is `EasyMongoWithContainer`,
which moves to easymongo so that easymongo depends on mongotest and not the
other way round.

So a test written against the guides today keeps compiling. What it gains
when the rework lands is a current image by default, a `mongosh` that exists,
a replica set with the name you asked for, a ++ctrl+c++ that terminates the
process, and a dial address that is right in CI.

## What to read

<div class="grid cards" markdown>

-   :material-cube-outline:{ .lg .middle } **[The mongod package](mongod.md)**

    ---

    The driver-free container lifecycle from PR #53: `Start`, `Options`,
    five named images, environment overrides, readiness and the reaper.

-   :material-rocket-launch-outline:{ .lg .middle } **[Start, Run and Attach](next-api.md)**

    ---

    The root API from #40: `mongotest.Run(t)` returns a `*mongo.Client` with a
    container attached.

-   :material-swap-horizontal:{ .lg .middle } **[Migrating](migration.md)**

    ---

    Each of today's calls next to its replacement, and what changes
    underneath even if you change nothing.

-   :material-lan:{ .lg .middle } **[Remote and nested daemons](remote-daemons.md)**

    ---

    How the dial address is resolved when the daemon is not on the loopback,
    and what is still open.

</div>
