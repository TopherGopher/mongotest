---
title: Conformance suite
description: The shared test and benchmark suites that every dockerclient implementation runs.
---

# Conformance suite

`github.com/tophergopher/mongotest/dockerclient/dockerclienttest` holds one
test suite and one benchmark suite. `dockerapi`, `mobyclient` and
`dockermock.Fake` all run them, so the three are held to the same behaviour
and measured on the same scale. A new implementation of the interface is
finished when it passes `Conformance`.

## Running it against your implementation

The suite takes a factory that returns a fresh client for each case. The
client should be pointed at a double, so the suite runs anywhere with no
daemon installed. The pattern `dockerapi` and `mobyclient` use is a real
client talking to a `dockermock.Daemon` whose routes are backed by a `Fake`,
so the whole HTTP path is exercised against state that actually changes:

```go
package myclient_test

import (
    "testing"

    "github.com/stretchr/testify/require"
    "github.com/tophergopher/mongotest/dockerclient"
    "github.com/tophergopher/mongotest/dockerclient/dockerclienttest"
    "github.com/tophergopher/mongotest/dockermock"

    "example.com/myclient"
)

func newConformanceClient(tb testing.TB) dockerclient.Client {
    tb.Helper()
    d := dockermock.NewDaemon()
    tb.Cleanup(d.Close)
    d.ServeFake(dockermock.NewFake())

    c, err := myclient.New(myclient.WithHost(d.Host()))
    require.NoError(tb, err, "a client against the fake daemon must construct")
    return c
}

func TestConformance(t *testing.T) {
    dockerclienttest.Conformance(t, func(t *testing.T) dockerclient.Client {
        return newConformanceClient(t)
    })
}

func BenchmarkClient(b *testing.B) {
    dockerclienttest.Benchmarks(b, func(b *testing.B) dockerclient.Client {
        return newConformanceClient(b)
    })
}
```

A `dockerclient.Client` that is itself a double can be passed straight in;
`dockermock.Fake` runs the suite that way.

## What is checked

The suite is five groups of subtests, each assertion carrying a message that
says what the expectation protects. The image it works with is
`dockerclienttest.TestImage` (`mongo:8`); nothing is downloaded.

| Group | What it proves |
| --- | --- |
| **Rejects bad input before reaching the daemon** | Every method returns `ErrInvalidArgument` for empty or malformed IDs, bad image references, relative destination directories, invalid file modes and port bindings without a protocol, without sending a request. |
| **Reports a missing object as `ErrNotFound`** | Inspecting, starting, removing or exec-ing against an unknown container, and creating from an image that is not local, all classify as `ErrNotFound`. |
| **Runs a container lifecycle** | Pull, create, start, inspect (running, with a published host port), top, remove, and remove-again-is-not-found, in order. |
| **Copies files into a container** | `CopyToContainer` and `CopyArchiveToContainer` place the given files with their modes, including into a container that is created but not yet started. |
| **Runs a command and reports its output** | `Exec` returns stdout and stderr separately and a non-zero exit code as data; `ExecCreate`, `ExecStartTo` and `ExecInspect` compose to the same result. |

Behaviour that depends on the wire, such as the exact request shape, pull
progress parsing, exec frame demultiplexing across split reads and the
negotiation window, is tested in each implementation's own package against
`dockermock.Daemon`, since a double like `Fake` has no wire.

## What is benchmarked

The benchmark suite measures the client's own cost: building the request,
encoding the body, decoding the reply. The daemon is in-process, so daemon and
network time never enter the numbers. Cases cover every method plus the
hot paths inside them (tar writing, stream demultiplexing, error
construction). Run them with:

```sh
go test ./dockerapi/ -run='^$' -bench=. -benchmem
cd mobyclient && go test -run='^$' -bench=. -benchmem
```

Comparing two implementations is a matter of running the same suite with the
same daemon, which is how the
[dockerapi against mobyclient numbers](mobyclient.md#choosing-between-dockerapi-and-mobyclient)
were produced.
