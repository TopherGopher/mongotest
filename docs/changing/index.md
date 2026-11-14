---
title: What's changing
description: The rework in progress, what each status badge means, and where each piece stands.
status: current
tracks: ["#48"]
---

# What's changing

mongotest is being reworked. The calls in the guides keep working: each stays
as a wrapper over the new API. Today, use `NewTestConnection`; `mongod` can be
used on its own once PR #53 merges.

This section describes the parts of the new API that are written. Each page
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
| `dockerclient`, `dockerapi`, `dockermock`, `mobyclient`, the conformance suite | on master | [Docker clients](../docker-clients/index.md) |
| `mongod`: start a container and get its URI | landing in [PR #53](https://github.com/TopherGopher/mongotest/pull/53) | [The mongod package](mongod.md) |
| The dial address behind a remote or nested daemon | landing in [PR #53](https://github.com/TopherGopher/mongotest/pull/53) | [Remote and nested daemons](remote-daemons.md) |

Everything not yet written is planned in GitHub issues. Issue
[#48](https://github.com/TopherGopher/mongotest/issues/48) lists them in
order.

## What stays the same

The old names stay as wrappers and are not deprecated
([#41](https://github.com/TopherGopher/mongotest/issues/41)). The one
exception is `EasyMongoWithContainer`, which moves to easymongo so that
easymongo depends on mongotest and not the other way round.
