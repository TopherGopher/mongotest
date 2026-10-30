---
title: Scripts and exec
description: Running a JavaScript file or any command inside the mongo container.
---

# Scripts and exec

Sometimes the easiest way to set up a test is the same way you would do it by
hand: open a shell on the server and type. `TestConnection` offers two ways in.

```go
func (tc *TestConnection) RunMongoScriptOnContainer(mongoScript string) (output string, err error)
func (tc *TestConnection) ExecCommandInMongoContainer(cmd []string) (output string, err error)
```

## Running a command

`ExecCommandInMongoContainer` runs any program that exists in the image and
returns everything it printed. A non-zero exit code is returned as an error,
with the output still populated so you can read what went wrong.

```go
out, err := conn.ExecCommandInMongoContainer([]string{
    "mongosh", "--quiet", "--eval", "db.getSiblingDB('app').stats().ok",
})
require.NoError(t, err)
require.Contains(t, out, "1")
```

Anything in the image is fair game. Useful ones:

| Command | What for |
| --- | --- |
| `mongosh --quiet --eval '<js>'` | Run a line of JavaScript on MongoDB 6 and newer. |
| `mongoimport --db app --collection roster --file /tmp/roster.json` | Load a fixture you copied in. |
| `mongod --version` | Confirm which server version `latest` resolved to. |
| `cat /proc/meminfo` | Debug a slow or dying container. |

## Running a script file

`RunMongoScriptOnContainer` takes JavaScript source, writes it to a file inside
the container and runs it with the shell. Multi-line scripts are fine.

```go
out, err := conn.RunMongoScriptOnContainer(`
    const db = db.getSiblingDB('app');
    db.roster.insertMany([
        { name: 'Diana', city: 'Themyscira' },
        { name: 'Bruce', city: 'Gotham' },
    ]);
    print(db.roster.countDocuments());
`)
require.NoError(t, err)
require.Equal(t, "2", strings.TrimSpace(out))
```

!!! warning "Uses the legacy `mongo` shell"
    The script is executed with `mongo --quiet <file>`. The official image
    dropped that binary in MongoDB 6.0 in favour of `mongosh`, so against
    `mongo:latest` this call fails with `executable file not found`. Either
    [pin an older image](replica-sets.md#current-limits) or use
    `ExecCommandInMongoContainer` with `mongosh` as shown above. Moving this
    call onto `mongosh` is tracked in
    [#35](https://github.com/TopherGopher/mongotest/issues/35).

## Seeding from a fixture file

Combine the two: copy a file in with `mongoimport` reading from standard input
is not possible through `Exec`, so write the data inline instead.

```go
docs := `{"name":"Diana","city":"Themyscira"}
{"name":"Bruce","city":"Gotham"}`

_, err := conn.ExecCommandInMongoContainer([]string{
    "sh", "-c", "printf '%s' '" + docs + "' | mongoimport --quiet --db app --collection roster",
})
require.NoError(t, err)
```

For anything larger than a few lines, insert through the driver instead; it
is faster and avoids quoting.

## Output handling

Both calls return the combined stdout and stderr of the process as one string,
with a trailing newline. Trim before comparing. The container's own log
(`mongod` output) is separate; read it with `docker logs <id>` using
`conn.MongoContainerID()`.
