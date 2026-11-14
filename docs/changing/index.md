---
title: What's changing
description: The rework in progress, what each status badge means, and where each piece stands.
status: current
tracks: ["#48"]
---

# What's changing

mongotest is being reworked. The calls in the guides keep working: each stays
as a wrapper over the new API. Today, use `NewTestConnection`. Once PR #53
merges, you can also start a container with the `mongod` package directly.

This section describes the parts of the new API that are implemented. Each page
on the site says how far along it is:

<div class="mt-legend" markdown>
<div><span class="mt-status__badge current">Current</span><span>Describes what is on <code>master</code> today.</span></div>
<div><span class="mt-status__badge changing">Changing</span><span>Works today, but the API on the page is being reworked. The old calls stay as wrappers.</span></div>
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

## The one call that moves

`EasyMongoWithContainer` moves to the easymongo module when
[#41](https://github.com/TopherGopher/mongotest/issues/41) lands, so that
easymongo depends on mongotest and not the other way round. Every other name
stays, and none is deprecated.
