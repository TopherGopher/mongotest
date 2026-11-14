---
title: Writing a new page
description: How this site is built, and the conventions for adding a guide.
---

# Writing a new page

This site is plain Markdown under `docs/`, built with
[MkDocs](https://www.mkdocs.org/) and the
[Material theme](https://squidfunk.github.io/mkdocs-material/). Every push to
`master` that touches `docs/` or `mkdocs.yml` rebuilds and publishes it.

## Preview locally

```sh
python3 -m venv .venv && . .venv/bin/activate
pip install -r docs/requirements.txt
mkdocs serve
```

Open <http://127.0.0.1:8000>. The page reloads as you save.
`mkdocs build --strict` is what CI runs; it fails on a broken link or a page
missing from the navigation, so run it before you push.

## Add a guide

1. Create `docs/guides/<slug>.md` with front matter:

    ```markdown
    ---
    title: Short title
    description: One sentence for search results and the card on the guides page.
    status: current
    tracks: ["#40"]
    ---

    # Short title

    One paragraph on what problem this pattern solves. Then the code.
    ```

2. Add it to `nav` in `mkdocs.yml` under **Guides**, in the position it
   belongs in the reading order.

3. Add a card for it on `docs/guides/index.md` so it is discoverable from the
   section landing page.

## Conventions

**Lead with working code.** A guide is a complete example first and
explanation second. Every snippet should compile if the surrounding imports
are obvious; say so when something is elided.

**Say what is true today, and badge it.** `status` is one of `current`,
`changing` or `landing`, and `tracks` lists the issues (`"#40"`)
and pull requests (`"PR #53"`) the page follows. The site renders a banner
and a nav icon from them. Where a feature has a limit, say so in a
`!!! warning` block right where the reader would trip over it, with the issue
number. The [replica sets guide](../guides/replica-sets.md#two-limits-today) is
the model, and [Keeping the docs current](../project/docs-maintenance.md) has
the checklist for flipping a badge when something lands.

**Use the theme's blocks.** Admonitions, tabs and code annotations are
described in the
[Material reference](https://squidfunk.github.io/mkdocs-material/reference/).

**Link to the generated reference for signatures.** Describe behaviour here;
let pkg.go.dev carry the exact types.

**One idea per section.** Headings are navigation. If a section needs a
second heading to stay readable, it is probably two sections.
