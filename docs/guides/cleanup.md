---
title: Cleanup and reaping
description: Everything that removes a mongotest container, in the order it is tried, and how to remove stray ones.
status: changing
tracks: ["PR #53", "#33"]
---

# Cleanup and reaping

A container that outlives its test keeps a port and memory. Four things
remove containers. You write the first; the others cover a test that never
reaches it.

## 1. `KillMongoContainer` in `t.Cleanup`

```go
conn, err := mongotest.NewTestConnection(true)
require.NoError(t, err)
t.Cleanup(func() { _ = conn.KillMongoContainer() })
```

`KillMongoContainer` force-removes the container and its anonymous volumes,
and forgets the container ID so a second call does nothing. Prefer `t.Cleanup` over `defer`:

| | `t.Cleanup` | `defer` |
| --- | --- | --- |
| Runs after `t.Fatal` | yes | yes |
| Runs after parallel subtests finish | yes | no, runs when the parent function returns |
| Runs after a panic in the test | yes, the testing package recovers first | yes |
| Runs when the process gets a signal | no | no |

The last row is why the others exist.

## 2. The signal handler

Importing mongotest installs a handler for `SIGINT`, `SIGTERM`, `SIGQUIT` and
`SIGABRT`. When one arrives, every container started in this process and not
yet removed is force-removed. This covers ++ctrl+c++ during a run.

!!! warning "Known issue: the process keeps running"
    The handler reaps the containers but does not re-raise the signal, so
    after ++ctrl+c++ the containers are gone yet the test binary is still
    alive. Press ++ctrl+c++ a second time, or let `go test` time out.
    [PR #53](https://github.com/TopherGopher/mongotest/pull/53) fixes this.

You can remove the same containers yourself, for instance from a `TestMain`:

```go
func TestMain(m *testing.M) {
    code := m.Run()
    mongotest.ReapRunningContainers()
    os.Exit(code)
}
```

## 3. The finalizer

Each `TestConnection` carries a `runtime.SetFinalizer` that removes the
container when the garbage collector finds the value unreachable. Finalizers
are not guaranteed to run before a program exits, so do not rely on this.

## 4. The label

Every container is created with the label `mongotest=regression`. Whatever
else fails, this lets you find and remove stray containers with one command:

```sh
docker rm --force $(docker ps -aq --filter label=mongotest=regression)
```

With nothing to remove, `docker rm` reports a missing argument, which is
harmless. To look before removing:

```sh
docker ps -a --filter label=mongotest=regression \
  --format 'table {{.ID}}\t{{.Names}}\t{{.Status}}\t{{.Ports}}'
```

Add the removal command to a CI job's always-run step, so containers left by
a crashed run do not hold ports and memory the next run needs:

```yaml
- name: Remove stray mongotest containers
  if: always()
  run: docker ps -aq --filter label=mongotest=regression | xargs -r docker rm --force
```

## What cannot be cleaned up

If the Go process is killed with `SIGKILL`, or the machine loses power, none
of the above runs. The containers stay until the command under
[the label](#4-the-label) removes them.

## Checking for leaks in a suite

A quick way to prove a suite is clean is to count before and after:

```sh
before=$(docker ps -aq --filter label=mongotest=regression | wc -l)
go test ./...
after=$(docker ps -aq --filter label=mongotest=regression | wc -l)
test "$before" -eq "$after" || echo "leaked $((after - before)) container(s)"
```
