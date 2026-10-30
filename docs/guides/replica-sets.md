---
title: Replica sets
description: Starting a single-member replica set for transactions and change streams.
---

# Replica sets

Transactions, change streams and some retryable write behaviour need a replica
set. A single `mongod` started with `--replSet` and initiated with one member
is enough for tests.

```go
func NewReplicaSetContainer(rsName string) (*TestConnection, error)
```

```go
conn, err := mongotest.NewReplicaSetContainer("rs0")
require.NoError(t, err)
t.Cleanup(func() { _ = conn.KillMongoContainer() })

sess, err := conn.MongoDriverClient().StartSession()
require.NoError(t, err)
defer sess.EndSession(ctx)

_, err = sess.WithTransaction(ctx, func(sc mongo.SessionContext) (interface{}, error) {
    coll := conn.MongoDriverClient().Database("bank").Collection("accounts")
    if _, err := coll.InsertOne(sc, bson.M{"owner": "a", "balance": 100}); err != nil {
        return nil, err
    }
    _, err := coll.InsertOne(sc, bson.M{"owner": "b", "balance": 0})
    return nil, err
})
require.NoError(t, err, "a transaction must commit on a single-member replica set")
```

What happens on top of the [normal lifecycle](../getting-started/how-it-works.md):

1. The container is started with `mongod --replSet <name>`.
2. A `rs.initiate(...)` script is copied into the container and run with the
   shell, retried a few times while `mongod` finishes starting.
3. The connection is opened with `directConnection=true`, so the driver talks
   to this one member without trying to discover others.

## Current limits

!!! warning "Read before relying on this"
    The replica set path is the oldest part of mongotest and is being
    reworked (issue [#32](https://github.com/TopherGopher/mongotest/issues/32)).
    Two things to know today:

    **It needs the legacy `mongo` shell.** The initiate script is run with
    `mongo`, which the official image stopped shipping in MongoDB 6.0. Against
    `mongo:latest` the initiation fails. Until the rework lands, pin an image
    that still has the shell by tagging it under the name mongotest looks for:

    ```sh
    docker pull mongo:5.0
    docker tag mongo:5.0 registry.hub.docker.com/library/mongo:latest
    ```

    mongotest finds that image locally and never pulls.

    **The replica set name is fixed to `cf`.** The `rsName` argument is passed
    to `mongod --replSet`, but the initiate script hardcodes `_id: 'cf'`, so
    the two disagree unless you pass `"cf"`. Pass `"cf"` for now.

## Change streams

With a replica set running, change streams work as they do in production:

```go
coll := conn.MongoDriverClient().Database("app").Collection("events")
stream, err := coll.Watch(ctx, mongo.Pipeline{})
require.NoError(t, err)
defer stream.Close(ctx)

_, err = coll.InsertOne(ctx, bson.M{"kind": "created"})
require.NoError(t, err)

require.True(t, stream.Next(ctx), "the insert must appear on the change stream")
```

## Where this is going

The rework replaces the shell script with a `replSetInitiate` command sent
over the driver, honours the requested name, polls `hello` until the member
reports `isWritablePrimary`, and exposes the whole thing as an option:

```go
inst := mongotest.Run(t, mongotest.WithReplicaSet("rs0"))
```

See the [roadmap](../project/roadmap.md) for the full shape of the new API.
