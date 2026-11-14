---
title: What's changing
description: The rework in progress, what each status badge means, and where each piece stands.
status: current
tracks: ["#48"]
---

# What's changing

mongotest is being reworked. Today, use `NewTestConnection`. Once PR #53
merges, you can also start a container with the `mongod` package directly.
[What stays and what goes](#what-stays-and-what-goes) says which of today's
calls survive the rework.

This section describes the parts of the new API that are implemented. Each page
on the site says how far along it is:

<div class="mt-legend" markdown>
<div><span class="mt-status__badge current">Current</span><span>Describes what is on <code>master</code> today.</span></div>
<div><span class="mt-status__badge changing">Changing</span><span>Works today, but the API on the page is being reworked.</span></div>
<div><span class="mt-status__badge landing">Landing</span><span>Implemented in an open pull request. Not on <code>master</code> yet; details may still move.</span></div>
</div>

The same badge appears as an icon next to the page in the navigation.

## Where things stand

| Piece | Status | Read |
| --- | --- | --- |
| The Docker client interface, its implementations and test doubles | Current | [Docker clients](../docker-clients/index.md) |
| `mongod`: start a container and get its URI | Landing in [PR #53](https://github.com/TopherGopher/mongotest/pull/53) | [The mongod package](mongod.md) |
| Connecting when the daemon is on another host, or the tests run in a container | Landing in [PR #53](https://github.com/TopherGopher/mongotest/pull/53) | [Remote and nested daemons](remote-daemons.md) |

Everything not yet written is planned in GitHub issues. Issue
[#48](https://github.com/TopherGopher/mongotest/issues/48) lists them in
order.

## What stays and what goes

These names stay, as wrappers over the new API, and none is deprecated
([#41](https://github.com/TopherGopher/mongotest/issues/41)):
`NewTestConnection`, `NewReplicaSetContainer`, `MongoClientWithContainer`,
`GetAvailablePort`, and on the connection `KillMongoContainer`,
`MongoContainerID`, `RunMongoScriptOnContainer` and
`ExecCommandInMongoContainer`.

The easymongo calls made through the connection go away. Today the connection
embeds an easymongo connection. With
[#40](https://github.com/TopherGopher/mongotest/issues/40) it embeds the
official `*mongo.Client` instead, so `conn.D("app").C("users")`,
`coll.Insert().One(...)` and `conn.MongoDriverClient()` stop compiling.
`EasyMongoWithContainer` moves to the easymongo module.

A test written with
[`MongoClientWithContainer`](../guides/one-shot-helpers.md) compiles unchanged
after the rework, because it is handed the official client. A test that makes
its database calls on `conn.MongoDriverClient()` has that one call to
replace.
