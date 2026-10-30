---
title: Cleanup and reaping
description: Everything that removes a mongotest container, in the order it is tried, and how to clear out strays.
status: changing
tracks: ["PR #53", "#33"]
---

# Cleanup and reaping

A container that outlives its test is a leak: it holds a port, a few hundred
megabytes of memory and, over a week of CI runs, a disk. mongotest has four
layers of defence. Only the first one is yours to write; the others are there
for when a test does not get to run it.

## 1. `KillMongoContainer` in `t.Cleanup`

```go
conn, err := mongotest.NewTestConnection(true)
require.NoError(t, err)
t.Cleanup(func() { _ = conn.KillMongoContainer() })
```

`KillMongoContainer` force-removes the container and its anonymous volumes,
deletes any temporary TLS files, and forgets the container ID so a second
call is a no-op. Prefer `t.Cleanup` over `defer`:

| | `t.Cleanup` | `defer` |
| --- | --- | --- |
| Runs after `t.Fatal` | yes | yes |
| Runs after parallel subtests finish | yes | no, runs when the parent function returns |
| Runs after a panic in the test | yes, the testing package recovers first | yes |
| Runs when the process is killed | no | no |

The last row is why the other layers exist.

## 2. The signal handler

Importing mongotest installs a handler for `SIGINT`, `SIGTERM`, `SIGQUIT` and
`SIGABRT`. When one arrives, every container started in this process and not
yet removed is force-removed. This is what saves you when you press
++ctrl+c++ in the middle of a run.

!!! warning "Known issue: the process keeps running"
    The handler reaps the containers but does not re-raise the signal, so
    after ++ctrl+c++ the containers are gone yet the test binary is still
    alive. Press ++ctrl+c++ a second time, or let `go test` time out.
    Installing the handler lazily and re-raising the signal is issue
    [#33](https://github.com/TopherGopher/mongotest/issues/33).

You can trigger the same sweep yourself, for instance from a `TestMain`:

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
are not guaranteed to run before a program exits, so treat this as a courtesy,
not a mechanism.

## 4. The label

Every container is created with the label `mongotest=regression`. Whatever
else fails, this lets you find and remove strays with one command:

```sh
docker rm --force $(docker ps -aq --filter label=mongotest=regression)
```

`docker ps -aq` with the filter prints nothing when there is nothing to
remove, in which case `docker rm` complains about a missing argument; that is
harmless. To look before removing:

```sh
docker ps -a --filter label=mongotest=regression \
  --format 'table {{.ID}}\t{{.Names}}\t{{.Status}}\t{{.Ports}}'
```

Add the removal command to a CI job's always-run step so one crashed run does
not starve the next:

```yaml
- name: Remove stray mongotest containers
  if: always()
  run: docker ps -aq --filter label=mongotest=regression | xargs -r docker rm --force
```

## What cannot be cleaned up

If the Go process is killed with `SIGKILL`, or the machine loses power, none
of the layers above run. The containers stay until the label sweep removes
them. Docker itself does nothing on its own: mongotest does not use
`--rm`, because a container that removes itself on exit also removes the
evidence when `mongod` crashes during a test.

## Checking for leaks in a suite

A quick way to prove a suite is clean is to count before and after:

```sh
before=$(docker ps -aq --filter label=mongotest=regression | wc -l)
go test ./...
after=$(docker ps -aq --filter label=mongotest=regression | wc -l)
test "$before" -eq "$after" || echo "leaked $((after - before)) container(s)"
```
