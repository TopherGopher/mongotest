---
title: Table-driven and parallel tests
description: Choosing between a container per case and a container per table, and running tests in parallel without collisions.
status: current
tracks: ["#40"]
---

# Table-driven and parallel tests

Every container listens on its own random loopback port, so tests that each
start a container can run in parallel without coordination. The question is
only how many containers you want to pay for.

## A container per test, in parallel

The simplest shape. Each test is fully isolated and `t.Parallel` is safe.

```go
func TestFindByCity(t *testing.T) {
    t.Parallel()
    conn := startMongo(t)
    // ...
}

func TestFindByName(t *testing.T) {
    t.Parallel()
    conn := startMongo(t)
    // ...
}

// startMongo is the three lines every test would otherwise repeat.
func startMongo(t *testing.T) *mongotest.TestConnection {
    t.Helper()
    conn, err := mongotest.NewTestConnection(true)
    require.NoError(t, err, "a mongo container must start before the test can run")
    t.Cleanup(func() { _ = conn.KillMongoContainer() })
    return conn
}
```

With a warm image cache a container is ready in about a second, and parallel
tests overlap those seconds. A package with twenty such tests at the default
`-parallel` (the number of CPUs) finishes its start-ups in a few seconds total.

## A container per table, a database per case

When a table has many small cases, start one container for the table and give
each case its own database. Cases still cannot see each other's data, and the
whole table pays one start-up.

```go
func TestRosterQueries(t *testing.T) {
    conn := startMongo(t)

    cases := []struct {
        name   string
        seed   []hero
        filter bson.M
        want   int
    }{
        {"by city", []hero{{Name: "Diana", City: "Themyscira"}}, bson.M{"city": "Themyscira"}, 1},
        {"no match", []hero{{Name: "Diana", City: "Themyscira"}}, bson.M{"city": "Gotham"}, 0},
        {"empty collection", nil, bson.M{}, 0},
    }

    for _, tc := range cases {
        t.Run(tc.name, func(t *testing.T) {
            t.Parallel()
            // A database name derived from the subtest name is unique and
            // readable in the server logs. Replace characters mongo rejects.
            db := conn.D(dbNameFor(t))
            t.Cleanup(func() { _ = db.Drop() })

            coll := db.C("roster")
            for i := range tc.seed {
                _, err := coll.Insert().One(&tc.seed[i])
                require.NoError(t, err, "seeding %q must succeed", tc.name)
            }

            var got []hero
            require.NoError(t, coll.Find(tc.filter).All(&got), "the query must run")
            assert.Len(t, got, tc.want, "the filter must match the seeded documents")
        })
    }
}

func dbNameFor(t *testing.T) string {
    return strings.NewReplacer("/", "_", " ", "_").Replace(t.Name())
}
```

!!! note "Why cleanup order matters here"
    The outer test registered `KillMongoContainer` before the subtests ran.
    `t.Cleanup` on the outer `t` waits for parallel subtests to finish, then
    runs in reverse order, so the subtests' `db.Drop()` calls all run before
    the container goes away.

## Choosing

| | Container per test | Container per table |
| --- | --- | --- |
| Isolation | Complete, including server state such as indexes and server parameters | Per database. Server-wide state is shared across cases |
| Start-up cost | One container per test | One container per table |
| `t.Parallel` | Safe at every level | Safe for subtests; the container outlives them |
| A crashed `mongod` | Affects one test | Fails the whole table |
| Good for | Integration tests of a feature | Many small query or codec cases |

## Running packages in parallel

`go test ./...` runs packages concurrently, up to `-p` at a time. Each
package's test binary gets its own containers and its own ports, so nothing
needs to change. The practical limit is CPU and memory: each `mongod`
reserves a few hundred megabytes. If a machine starts swapping, lower `-p` or
`-parallel` rather than sharing containers between packages.

## Avoiding port collisions

mongotest asks the kernel for a free port and then releases it before Docker
binds it, so there is a small window in which another process could take the
same port. In practice this does not happen on a developer machine. On a very
busy CI host, a start that fails with a port-in-use error can be retried; the
next attempt picks a different port.
