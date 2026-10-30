---
title: Continuous integration
description: Running a mongotest suite under GitHub Actions and other hosted runners.
status: changing
tracks: ["#52", "#46"]
---

# Continuous integration

mongotest needs a Docker-compatible daemon reachable from the test process.
Most hosted Linux runners have one. This page covers the shapes that work and
the one that does not.

## GitHub Actions

`ubuntu-latest` ships with Docker running and the current user in the
`docker` group, so the test job needs nothing special:

```yaml title=".github/workflows/go.yml"
name: Go

on:
  push:
    branches: [ master ]
  pull_request:

jobs:
  test:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v4

      - uses: actions/setup-go@v5
        with:
          go-version-file: go.mod

      # Optional: pull once up front so the first test is not also a download.
      - name: Pull mongo
        run: docker pull mongo

      - name: Test
        run: go test -race -count=1 ./...

      - name: Remove stray mongotest containers
        if: always()
        run: docker ps -aq --filter label=mongotest=regression | xargs -r docker rm --force
```

This is close to the workflow this repository runs on itself; see
[`.github/workflows/go.yml`](https://github.com/TopherGopher/mongotest/blob/master/.github/workflows/go.yml).

### Using a service container instead

If you would rather not start containers from inside the test, run MongoDB as
a job service and use `NewTestConnection(false)`:

```yaml
jobs:
  test:
    runs-on: ubuntu-latest
    services:
      mongo:
        image: mongo:8
        ports:
          - 27017:27017
    steps:
      - uses: actions/checkout@v4
      - uses: actions/setup-go@v5
        with:
          go-version-file: go.mod
      - run: go test -race -count=1 ./...
```

The trade-off is isolation: every test shares one server. Give each test its
own database name and drop it in cleanup, as shown in
[Test connections](test-connection.md#without-a-container).

A tidy pattern is to decide at runtime:

```go
func startMongo(t *testing.T) *mongotest.TestConnection {
    t.Helper()
    useContainer := os.Getenv("MONGO_SERVICE") == ""
    conn, err := mongotest.NewTestConnection(useContainer)
    require.NoError(t, err)
    t.Cleanup(func() { _ = conn.KillMongoContainer() })
    return conn
}
```

## Other runners

=== "GitLab CI"

    Use a runner with the Docker socket mounted, or the shell executor on a
    host that runs Docker. Docker-in-Docker (`docker:dind` as a service) does
    **not** work with the current release; see the note below.

    ```yaml
    test:
      image: golang:1.27
      tags: [docker-socket]   # a runner that mounts /var/run/docker.sock
      script:
        - go test -race -count=1 ./...
    ```

=== "Self-hosted"

    Anything with a daemon the test process can reach. Set `DOCKER_HOST` if
    the socket is not at the default path, and make sure the runner user is in
    the `docker` group.

=== "macOS and Windows runners"

    GitHub's macOS runners do not ship Docker. Windows runners run Windows
    containers by default and cannot run the Linux `mongo` image. Run the
    mongotest suite on Linux and restrict other platforms to tests that use
    `NewTestConnection(false)` against a service, or skip them.

## The shape that does not work: a remote daemon

mongotest publishes the container port on `127.0.0.1` **of the machine the
daemon runs on** and then connects to `127.0.0.1` **from the test process**.
Those are the same machine in every setup above. They are not the same
machine when:

- the test runs inside a container and talks to the host's daemon over a
  mounted socket (the port is published on the host, not inside your
  container);
- `DOCKER_HOST` points at `tcp://some-other-box:2376`;
- a docker-in-docker service runs the daemon in a sibling container.

In those cases the connection is refused. Resolving a reachable address rather
than assuming loopback is in
[PR #53](https://github.com/TopherGopher/mongotest/pull/53), with the rest of
the work under [#52](https://github.com/TopherGopher/mongotest/issues/52);
[Remote and nested daemons](../changing/remote-daemons.md) explains how the
address will be chosen. Until the root package is on it, run the tests on the
same host as the daemon.

## Tips for stable CI runs

- **Pin the image.** `mongo:latest` changes under you. Pre-pull and tag a
  known version under the name mongotest uses:

    ```sh
    docker pull mongo:8.0
    docker tag mongo:8.0 registry.hub.docker.com/library/mongo:latest
    ```

- **Use `-count=1`.** Integration tests should not be served from the test
  cache.
- **Always sweep.** The label-based removal step with `if: always()` costs
  nothing and prevents a failed run from leaving containers for the next one
  on a persistent runner.
- **Watch memory.** Each `mongod` wants a few hundred megabytes. On a
  two-core runner with `-parallel` at its default, a dozen simultaneous
  containers is a lot. Lower `-parallel` before sharing containers.
