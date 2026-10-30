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

**Say what is true today.** Where a feature has a limit, say so in a
`!!! warning` block right where the reader would trip over it, with the issue
number. The [replica sets guide](../guides/replica-sets.md#current-limits) is
the model.

**Use the tools the theme gives you.**

=== "Admonitions"

    ```markdown
    !!! tip "Pull the image once"
        The first run downloads `mongo`; later runs are offline.

    ??? failure "Collapsed by default: error text as the title"
        Diagnosis and fix.
    ```

=== "Tabs"

    ```markdown
    === "easymongo"

        ```go
        coll := c.D("app").C("roster")
        ```

    === "official driver"

        ```go
        coll := m.Database("app").Collection("roster")
        ```
    ```

    Tabs with the same labels stay in sync across the page.

=== "Code annotations"

    ```markdown
    ```go
    conn, err := mongotest.NewTestConnection(true) // (1)!
    ```

    1. A numbered note that appears when the marker is clicked.
    ```

=== "Diagrams"

    ````markdown
    ```mermaid
    sequenceDiagram
        T->>M: NewTestConnection(true)
        M->>D: create container
    ```
    ````

**Link to the generated reference for signatures.** Describe behaviour here;
let pkg.go.dev carry the exact types.

**One idea per section.** Headings are navigation. If a section needs a
second heading to stay readable, it is probably two sections.

## Layout of the site

```
mkdocs.yml                 navigation, theme settings, Markdown extensions
docs/
  index.md                 landing page (rendered by overrides/home.html)
  getting-started/         install, quickstart, how it works
  guides/                  one page per usage pattern
  docker-clients/          dockerclient, dockerapi, mobyclient, dockermock
  reference/               map of the API, environment, this page
  project/                 roadmap, contributing, license
  overrides/home.html      the landing page template
  stylesheets/extra.css    colours, type and the landing page styles
  assets/                  logo and favicon
  requirements.txt         pinned Python dependencies for the build
.github/workflows/docs.yml build on every change, deploy from master
```
