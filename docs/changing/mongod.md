---
title: The mongod package
description: "Start a MongoDB container and get its URI, with no MongoDB driver imported. In PR #53."
status: landing
tracks: ["PR #53", "#32", "#33"]
---

# The mongod package

`github.com/tophergopher/mongotest/mongod` starts a MongoDB container, waits
until it answers, tells you where to connect, and removes it if the process
is interrupted. It imports no MongoDB driver.

Tests use [`NewTestConnection`](../guides/test-connection.md) today. Reach
for `mongod` once PR #53 merges, when you want the container without a
driver.

```go
c, err := mongod.Start(ctx)
if err != nil {
    return err
}
defer c.Stop(context.Background())

client, err := mongo.Connect(options.Client().ApplyURI(c.URI())) // driver v2; v1 takes a ctx first
```

With no options that runs `mongo:8.0`, publishes `mongod` on a host port the
daemon picks, labels the container `mongotest=regression`, and waits up to a
minute.

## Options

Options apply in the order given, so a later one overrides an earlier one:

```go
mongod.Start(ctx, mongod.WithImage("7.0"), mongod.WithReplicaSet("rs0"))
```

| Option | Effect |
| --- | --- |
| `WithImage(ref)` | The image, from a version (`"7.0"`), a repository, or a full reference. Only the parts it names change. |
| `WithMongoImage(img)` | The whole image, usually one of the [known ones](#which-image). |
| `WithReplicaSet(name)` | A single-member [replica set](#replica-sets), ready for writes. |
| `WithAuth(user, password)`, `WithoutAuth()` | See [Who can connect](#who-can-connect). |
| `WithPort(port)` | A fixed host port. Prefer the default: a port found free can be taken before the container binds it. |
| `WithHostIP(host)` | The address containers are reachable at; see [Remote and nested daemons](remote-daemons.md). |
| `WithStartTimeout(d)` | How long `Start` waits. The default is one minute. |
| `WithMongodArgs(args...)` | Extra flags for `mongod`. |
| `WithName(name)`, `WithLabel(key, value)` | The container's name, and labels beside the required one. |
| `WithLogger(l)` | Where a start is logged. `*slog.Logger` works; the default discards. |
| `WithDocker(client)` | A specific Docker client, such as a [double](#testing-code-that-starts-containers). |

Three settings can come from the environment, so CI can set them for a whole
suite. An option beats the variable, and the variable beats the default.

| Variable | Sets |
| --- | --- |
| `MONGOTEST_IMAGE` | The image, in any shape `WithImage` accepts. |
| `MONGOTEST_START_TIMEOUT` | The start timeout, as a Go duration such as `90s`. |
| `MONGOTEST_HOST_IP` | The address containers are reachable at. |

A version from an option combines with an image from the environment:
`WithImage("7.0")` under `MONGOTEST_IMAGE=mirror.corp.example/mongo` runs
`mirror.corp.example/mongo:7.0`.

## Which image

Five images are known:

| Value | Runs | Tags like `8` | Takes `mongod` flags |
| --- | --- | --- | --- |
| `ImageDockerHub` | `mongo:8.0` | yes | yes |
| `ImagePublicECR` | `public.ecr.aws/docker/library/mongo:8.0` | yes | yes |
| `ImageCommunity` | `mongodb/mongodb-community-server:8.0-ubi9` | no | yes |
| `ImageEnterprise` | `mongodb/mongodb-enterprise-server:8.0-ubi9` | no | yes |
| `ImageAtlasLocal` | `mongodb/mongodb-atlas-local:8.0` | yes | no |

`Start` refuses two mistakes before it creates anything:

- A bare version such as `8` for `ImageCommunity` or `ImageEnterprise`, whose
  tags all name an OS variant such as `8.0-ubi9`: `ErrNoSuchVersion`.
- `WithReplicaSet` or `WithMongodArgs` for `ImageAtlasLocal`, which runs its
  own replica set and takes no `mongod` flags: `ErrUnsupportedForImage`.

An image the package does not know is treated like the official one. To pull
a known image through a mirror, change its registry:

```go
mongod.WithMongoImage(mongod.ImageDockerHub.WithRegistry("mirror.corp.example"))
```

## The container

| Method | Returns |
| --- | --- |
| `URI()` | `mongodb://<host>:<port>/?directConnection=true`, with `<user>:<password>@` when the container has credentials. |
| `Host()`, `Port()` | The address to dial. The host is not always loopback; see [Remote and nested daemons](remote-daemons.md). |
| `ID()`, `Name()`, `Image()` | What was started. |
| `Username()`, `Password()` | The root user, or empty for a `mongod` that does not authenticate. `URI()` already includes them. |
| `ReplicaSet()` | The name given to `WithReplicaSet`, or empty. |
| `Stop(ctx)` | Removes the container and its volumes. Safe to call twice. |

## When Start returns

`Start` returns once `mongod` has answered a command on the address in the
URI, so the first command you send works. An image that creates a user first
runs a temporary `mongod`; `Start` waits for the one that replaces it, and for
the image's own health check if it declares one.

A start that fails removes the container it created. The error says which
step failed, and matches a sentinel such as `ErrNotReady` or
`ErrContainerExited` with `errors.Is`.

## Who can connect

A `mongod` with no users accepts anyone who can reach its port.

A container published on loopback gets no credentials. One published on every
interface, which is what a remote daemon, a sibling container or a
`WithHostIP` other than loopback needs, gets a root user, `mongotest`, with a
random password. `URI()` includes the credentials, so a test that takes its
connection string from the container runs unchanged in both places.

`WithAuth` sets credentials of your own, and `WithoutAuth` leaves a widely
published `mongod` open on a network you trust. Creating the user makes a
start a few seconds slower, which is why a loopback container gets none
unless you ask.

If the image does not create the user, `Start` removes the container and
returns `ErrAuthNotEnforced` rather than hand back an open `mongod`. Anyone
who can use the daemon can read the credentials, and traffic is not
encrypted.

## Replica sets

`WithReplicaSet("rs0")` starts `mongod` as the only member of a replica set,
which is what transactions and change streams need. `Start` initiates the set
and returns once it accepts writes. With credentials it also supplies the key
file `mongod` then requires.

Initiating uses the image's own `mongosh`. An image without it fails with
`ErrNotInitiated`.

## Cleaning up

`Stop` removes the container. A container that is never stopped is removed
when the process gets `SIGINT` or `SIGTERM`, and the process then dies of
that signal. `SIGKILL` cannot be handled.

To remove whatever is left when a run ends normally, call
`ReapRunningContainers` after `m.Run`:

```go
func TestMain(m *testing.M) {
    code := m.Run()
    if err := mongod.ReapRunningContainers(context.Background()); err != nil {
        log.Print(err)
    }
    os.Exit(code)
}
```

Anything that still escapes carries the label, so one command finds it:

```sh
docker rm -f $(docker ps -aq --filter label=mongotest=regression)
```

## Not here yet

- **TLS.** `WithTLS()` only adds `tls=true` to the URI. Certificates are issue
  [#34](https://github.com/TopherGopher/mongotest/issues/34), so a driver
  cannot yet connect to a container started with it.
- **Exec and scripts.** `Container` has no exec method; use the
  `dockerclient.Client` you passed. Issue
  [#35](https://github.com/TopherGopher/mongotest/issues/35) adds `Exec` and
  `RunScript`.
- **Data that outlives the container.** Issue
  [#55](https://github.com/TopherGopher/mongotest/issues/55).

## Testing code that starts containers

Pass a double with `WithDocker` and no daemon is needed. `Start` still sends
`mongod` a command, so the double has to publish a port something answers
on; `mongod/example_test.go` shows one way.
[dockermock](../docker-clients/dockermock.md) describes the doubles.
