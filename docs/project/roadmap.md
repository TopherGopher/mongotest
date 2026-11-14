---
title: Roadmap
description: Where the plan for mongotest lives.
status: current
tracks: ["#48"]
---

# Roadmap

mongotest is being reworked into a smaller module, without breaking today's
calls. The container handling needs no MongoDB driver, and the test helpers
on top of it need only the official one. The work is planned
and tracked in GitHub issues;
[#48](https://github.com/TopherGopher/mongotest/issues/48) lists them in
order.

[What's changing](../changing/index.md) describes the parts that are already
written.
