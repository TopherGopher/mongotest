---
title: Keeping the docs current
description: How the site tracks a fast-moving codebase, and the checklist for updating it when an issue or pull request lands.
status: current
---

# Keeping the docs current

Each page says how current it is. This page says how to keep that true.

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
3. **The table on [What's changing](../changing/index.md):** update the row.
4. **Code samples.** Any snippet that used a workaround for the old behaviour
   (tagging an image under the `latest` name, passing `"cf"` as a replica set
   name, `ExecCommandInMongoContainer` with `mongosh` instead of
   `RunMongoScriptOnContainer`) should become the straightforward version.

When the last `landing` page is promoted, delete the
`announce` block from `docs/overrides/main.html` and retire the section.

To see which pages a merge touches, list the front matter:

```sh
grep -rn '^status:\|^tracks:' docs/
```

## Checking before you push

`mkdocs build --strict` fails on a broken link or a page missing from the
navigation, and the Pages workflow runs it on every pull request. It does not
compile Go snippets. When a snippet is meant to be exact, prefer copying it
from a runnable `Example` test in the repository, which CI does compile, and
say which file it came from.
