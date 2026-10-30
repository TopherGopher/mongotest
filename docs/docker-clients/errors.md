---
title: Errors
description: The error sentinels and typed carriers every Docker client implementation reports.
---

# Errors

Every implementation of `dockerclient.Client` reports failures as one of a
small set of sentinels, or as a typed value that wraps one. Branch with
`errors.Is` or `errors.As`; never parse a message. Messages state what went
wrong, the value at fault and what to do about it, and are free to change.

## Sentinels

| Sentinel | Carrier type | Meaning |
| --- | --- | --- |
| `ErrInvalidArgument` | `*InvalidArgumentError` | A value was rejected before any request was sent. The error names the argument, the problem and the fix. |
| `ErrConnectionFailed` | `*ConnectionError` | The daemon could not be reached. Names the socket or address that was tried. |
| `ErrAPIVersion` | `*APIVersionError` | The client's and daemon's API version windows do not overlap, or a feature needs a newer daemon. Names the product, so Podman users are not told to upgrade Docker. |
| `ErrNotFound` | `*StatusError` | The daemon answered 404. For `ContainerCreate` this means the image is not local: pull and retry. |
| `ErrConflict` | `*StatusError` | The daemon answered 409, for instance a container name already in use. |
| `ErrUnauthorized` | `*StatusError` | The daemon answered 401 or 403. |
| `ErrDaemonResponse` | `*ResponseError` | The daemon answered with a status or body the client could not interpret. |
| `ErrStream` | `*StreamError` | An exec output stream reported a problem mid-way. |
| `ErrPull` | `*PullError` | The pull progress stream carried an error line from the daemon. |
| `ErrExecUnfinished` | | An exec process was still running when the client stopped waiting for its exit code. |

Three fixed-message cases are predeclared values so they can be compared
directly:

| Value | Wraps | When |
| --- | --- | --- |
| `ErrNoCommand` | `ErrInvalidArgument` | `Exec` or `ExecCreate` was given no program. |
| `ErrNoFiles` | `ErrInvalidArgument` | `CopyToContainer` was given an empty list. |
| `ErrEmptyHost` | `ErrInvalidArgument` | `WithHost("")`. |

## Branching

```go
id, _, err := docker.ContainerCreate(ctx, "", cfg)
switch {
case err == nil:
case dockerclient.IsNotFound(err):          // shorthand for errors.Is(err, ErrNotFound)
    // pull the image and try again
case errors.Is(err, dockerclient.ErrConflict):
    // the name is taken; remove the old container or pick another name
case errors.Is(err, dockerclient.ErrConnectionFailed):
    // no daemon; tell the user where we looked
default:
    return err
}
```

## Reading the details

Each carrier exposes what it knows. `errors.As` gets you there:

```go
var se *dockerclient.StatusError
if errors.As(err, &se) {
    log.Printf("%s %s returned %d: %s", se.Method, se.Path, se.StatusCode, se.Message)
}

var ve *dockerclient.APIVersionError
if errors.As(err, &ve) {
    log.Printf("%s supports %s to %s; this client supports %s to %s",
        ve.Product, ve.ServerMin, ve.ServerMax, ve.ClientMin, ve.ClientMax)
}
```

`StatusError` also carries a hint, a sentence suggesting the usual fix for
that status on that endpoint, which is included in the message.

## Validation happens first

Both implementations and the `Fake` double apply the same argument checks
before anything is sent: container and exec IDs must be non-empty and
well-formed, image references must parse, destination directories must be
absolute, file modes must be valid, port bindings must name a protocol. This
means a test against `dockermock.NewFake()` rejects the same bad input a real
daemon would, and the error is identical.

## The root package's own errors

The root `mongotest` package predates this scheme and has three sentinels of
its own: `ErrFailedToConnectToDockerDaemon`, `ErrNoAvailablePorts` and
`ErrMongoContainerAlreadyRunning`. They are listed in
[Test connections](../guides/test-connection.md#errors). When the root package
moves onto `dockerclient`, Docker failures will surface as the sentinels above.
