---
title: Exporting data
description: Turning a populated test database into a JSON fixture with DatabaseExporter.
status: changing
tracks: ["#43"]
---

# Exporting data

`DatabaseExporter` writes a collection to a file with `mongoexport`, so a
test's data can be kept as a fixture.

```go
func (tc *TestConnection) DatabaseExporter(dbName, collectionName string) *DatabaseExporter
```

The exporter is a builder. Set what you need, then call a terminal method:

```go
path, err := conn.DatabaseExporter("app", "roster").
    Query(`{"city": "Gotham"}`).
    Sort(`{"name": 1}`).
    Limit(100).
    ToJSONFile(true, true, false, false)
require.NoError(t, err)
t.Cleanup(func() { _ = os.Remove(path) })

data, err := os.ReadFile(path)
require.NoError(t, err)
```

## Builder methods

| Method | Effect |
| --- | --- |
| `Query(json)` | A filter, as a JSON string in `mongoexport` syntax. |
| `Sort(json)` | A sort specification, as JSON. |
| `Skip(n)` / `Limit(n)` | Paging. |
| `Database(name)` / `Collection(name)` | Override the names given to `DatabaseExporter`. |
| `Filepath(path)` | Write here instead of a temporary file. See the note below. |

## Terminal methods

### `ToJSONFile(prettify, jsonArray, relaxedJSON, compressToGZIP bool)`

Writes JSON and returns the path.

| Argument | `true` | `false` |
| --- | --- | --- |
| `prettify` | Indented, one field per line | Compact |
| `jsonArray` | One JSON array | One document per line (JSON Lines) |
| `relaxedJSON` | Relaxed Extended JSON (`{"n": 1}`) | Canonical Extended JSON (`{"n": {"$numberInt": "1"}}`) |
| `compressToGZIP` | gzip the output | Plain text |

Canonical JSON round-trips every BSON type exactly and is the right choice
for fixtures you will import again. Relaxed JSON is easier to read in a diff.

### `CSVFile()`

Writes CSV and returns the path. `mongoexport` requires an explicit field list
for CSV, and the builder does not expose one yet, so this call currently fails
at export time. Use JSON until the field list is added.

## Where the file goes

With no `Filepath`, the export lands in the system temporary directory with a
name such as `mongoexport-app-roster-123456.json`. Remove it in `t.Cleanup`
or move it into your `testdata` directory when you mean to keep it.

!!! bug "`Filepath` is unreliable today"
    The current implementation opens the given path read-only, so writing to
    it fails. Leave `Filepath` unset, take the temporary path that is
    returned, and `os.Rename` it where you want it. This is tracked with the
    exporter's move into its own module ([#43](https://github.com/TopherGopher/mongotest/issues/43)).

## Dependency note

The exporter is moving to its own module, issue
[#43](https://github.com/TopherGopher/mongotest/issues/43).
