---
title: One-shot helpers
description: EasyMongoWithContainer and MongoClientWithContainer run a function against a fresh server and clean up afterwards.
status: changing
tracks: ["#41"]
---

# One-shot helpers

Two functions wrap the whole lifecycle around a callback: start a container,
connect, run your function, remove the container, return your function's
error. There is no `TestConnection` to hold and nothing to remember to clean
up.

```go
func EasyMongoWithContainer(f func(c *easymongo.Connection) error) error
func MongoClientWithContainer(f func(m *mongo.Client) error) error
```

Pick the one whose argument type your code already speaks.

=== "easymongo"

    ```go
    func TestRoster(t *testing.T) {
        err := mongotest.EasyMongoWithContainer(func(c *easymongo.Connection) error {
            coll := c.D("heroes").C("roster")
            if _, err := coll.Insert().One(&hero{Name: "Diana"}); err != nil {
                return err
            }
            n, err := coll.Count()
            if err != nil {
                return err
            }
            if n != 1 {
                return fmt.Errorf("want 1 document, got %d", n)
            }
            return nil
        })
        require.NoError(t, err)
    }
    ```

=== "official driver"

    ```go
    func TestRoster(t *testing.T) {
        ctx := context.Background()
        err := mongotest.MongoClientWithContainer(func(m *mongo.Client) error {
            coll := m.Database("heroes").Collection("roster")
            if _, err := coll.InsertOne(ctx, hero{Name: "Diana"}); err != nil {
                return err
            }
            n, err := coll.CountDocuments(ctx, bson.D{})
            if err != nil {
                return err
            }
            if n != 1 {
                return fmt.Errorf("want 1 document, got %d", n)
            }
            return nil
        })
        require.NoError(t, err)
    }
    ```

## When to use them

The helpers shine for short, self-contained checks: a repository method, a
migration, a query you want to prove against a real server. The callback shape
makes the scope of the container obvious, and an early `return err` is enough
to fail the test with the container already gone.

They are less convenient when the test needs the server across several
subtests, needs the container ID, or wants to run a script inside the
container. For those, use [`NewTestConnection`](test-connection.md).

## Mixing in testing assertions

The callback returns an error rather than taking a `*testing.T`, so `require`
cannot be used inside it without capturing `t` from the enclosing scope.
That works, and `require.NoError` will still stop the test:

```go
err := mongotest.MongoClientWithContainer(func(m *mongo.Client) error {
    _, err := m.Database("heroes").Collection("roster").InsertOne(ctx, hero{Name: "Diana"})
    require.NoError(t, err, "insert must succeed on a fresh collection")
    return nil
})
require.NoError(t, err)
```

`require` ends the test goroutine with `runtime.Goexit`, which still runs
deferred functions, so the container is removed on the way out.

## Running the callback more than once

Each call starts a new container, so a loop over cases pays the start-up cost
per iteration:

```go
for _, tc := range cases {
    err := mongotest.MongoClientWithContainer(func(m *mongo.Client) error {
        return runCase(m, tc)
    })
    require.NoError(t, err, tc.name)
}
```

If that is too slow, move the loop inside the callback and give each case its
own database; see [Table-driven and parallel tests](parallel-tests.md).
