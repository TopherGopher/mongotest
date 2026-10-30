---
title: Keeping the docs current
description: How the site tracks a fast-moving codebase, and the checklist for updating it when an issue or pull request lands.
status: current
---

# Keeping the docs current

The code is changing faster than any one page can follow, so the site is
built to say how current each page is rather than to pretend it is all
current. This page is the maintenance contract.

## The status badge

Every page declares a status in its front matter, and the site renders it as
a banner under the title and an icon in the navigation:

```yaml
---
title: Replica sets
status: changing
tracks: ["#40"]
---
```

| Status | Use it when |
| --- | --- |
| `current` | The page describes what is on `master`. |
| `changing` | The page describes what works today, and an issue or pull request is reworking it. The old calls stay as wrappers. |
| `landing` | The page describes code in an open pull request. |
| `planned` | The page describes an API specified in an issue and not yet implemented. |

`tracks` lists the issues and pull requests the page follows. Write issues as
`"#40"` and pull requests as `"PR #53"`; the banner links them. A page with
no `status` shows no banner, which is fine for pages that are not about the
API, such as this one.

## When a pull request merges

Search the docs for its number and work through the hits:

```sh
grep -rn 'PR #53' docs/ mkdocs.yml
```

1. **Pages badged `landing` on that PR** become `current`. Move the page out
   of **What's changing** into the section where it belongs for good, and
   update `nav` in `mkdocs.yml`. Replace prose that says "lands in" or "will"
   with the present tense.
2. **Pages badged `changing`** that the PR affects: rewrite the "today"
   description to the new behaviour, delete the known-limit admonitions the
   PR fixed, and drop the number from `tracks` or set `status: current`.
3. **The table on [What's changing](../changing/index.md)** and the
   [roadmap](roadmap.md): update the row.
4. **Code samples.** Any snippet that used a workaround for the old behaviour
   (tagging an image under the `latest` name, passing `"cf"` as a replica set
   name, `ExecCommandInMongoContainer` with `mongosh` instead of
   `RunMongoScriptOnContainer`) should become the straightforward version.

When the last `landing` page is promoted and nothing is `planned`, delete the
`announce` block from `docs/overrides/main.html` and retire the section.

## When an issue is specified or changes

A new issue that changes usage gets a page or a section under **What's
changing**, badged `planned`, written from the issue body. The point is that
the shape can be read against the rest of the docs before it is built. When
the issue body changes, the page changes; the page is not a second source of
truth, it is a readable view of the first one.

## Which pages track what

| Page | Status | Tracks |
| --- | --- | --- |
| [How it works](../getting-started/how-it-works.md) | changing | PR #53, #40 |
| [Test connections](../guides/test-connection.md) | changing | #40, #41 |
| [One-shot helpers](../guides/one-shot-helpers.md) | changing | #41 |
| [Replica sets](../guides/replica-sets.md) | changing | #40 |
| [Scripts and exec](../guides/scripts-and-exec.md) | changing | #35 |
| [Exporting data](../guides/exporting-data.md) | changing | #43 |
| [Cleanup and reaping](../guides/cleanup.md) | changing | PR #53, #33 |
| [Continuous integration](../guides/ci.md) | changing | #52, #46 |
| [Podman](../guides/podman.md) | current | #51 |
| [Environment variables](../reference/environment.md) | changing | PR #53 |
| [The mongod package](../changing/mongod.md) | landing | PR #53, #32, #33 |
| [Remote and nested daemons](../changing/remote-daemons.md) | landing | PR #53, #52 |
| [Start, Run and Attach](../changing/next-api.md) | planned | #40, #41, #42, #36 |
| [Migrating](../changing/migration.md) | planned | #41, #40 |
| Docker client pages | current | |

Keep this table in step with the front matter; it is the quickest way to see
what a merge touches.

## Checking before you push

`mkdocs build --strict` fails on a broken link or a page missing from the
navigation, and the Pages workflow runs it on every pull request. It does not
compile Go snippets. When a snippet is meant to be exact, prefer copying it
from a runnable `Example` test in the repository, which CI does compile, and
say which file it came from.
