---
title: One-shot helpers
description: EasyMongoWithContainer and MongoClientWithContainer run a function against a fresh server and clean up afterwards.
status: changing
tracks: ["#41"]
---

# One-shot helpers

Two functions start a container, run your function against it, remove the
container and return your function's error.

```go
func EasyMongoWithContainer(f func(c *easymongo.Connection) error) error
func MongoClientWithContainer(f func(m *mongo.Client) error) error
```

Use the one that passes the client your code takes.

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

Use them for a check that needs a server for one function call. Use
[`NewTestConnection`](test-connection.md) when the test needs the server
across subtests, the container ID, or a script run inside the container.

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

Each call starts a new container. To run many cases against one, loop inside
the callback and give each case its own database; see
[Table-driven and parallel tests](parallel-tests.md).
