---
title: Contributing
description: How to build, test and propose changes to mongotest, and the standard changes are reviewed against.
---

# Contributing

Issues and pull requests are welcome at
[github.com/TopherGopher/mongotest](https://github.com/TopherGopher/mongotest).
This page covers how to get a working tree going and what a change is
expected to look like.

## Build and test

```sh
git clone https://github.com/TopherGopher/mongotest
cd mongotest

gofmt -l .                              # must print nothing
go vet ./...
go test -race ./...                     # root module

cd mobyclient && go test -race ./...    # separate module, not reached by ./...
```

Integration tests need a Docker daemon and the `mongo:8` image, and **fail
rather than skip** when the daemon is unreachable. That is deliberate: a
skipped integration test is a green run that proved nothing.

Benchmarks and fuzzing:

```sh
go test ./... -run='^$' -bench=. -benchmem
go test ./dockerclient/ -run='^$' -fuzz=FuzzCheckID -fuzztime=30s
```

## Things that will bite

- **`go.work` cannot be committed yet.** It breaks the root build, and
  `go work sync` rewrites `go.mod` and `go.sum` so the breakage survives
  deleting the workspace file. Recovery is `git checkout -- go.mod go.sum`.
  The reason and the fix are in the [roadmap](roadmap.md).
- **`mobyclient/go.mod` carries a `replace`** to the parent working tree
  until a tag contains `dockerclient`. It is annotated in the file.
- **The old root implementation** still compiles against
  `github.com/docker/docker v20.10.17`. Leave it alone until issue #44.

## Coding style

These are the standard a change is reviewed against, not aspirations.

**Test-driven.** Write the test first, run it, confirm it fails for the
reason you expect, then implement until it passes. A test is never deleted,
skipped or weakened to get a green run. When a test must change because the
public API changed, only the types change, never the assertion.

**testify, with a message on every assertion.** `require` for preconditions
that make the rest of the test meaningless, `assert` for the checks
themselves. Every call carries a final message saying what the expectation
protects, in words a reader who did not write the test can act on: not
"expected true" but "the archive is streamed, so no Content-Length is known
up front". Compare typed values field by field rather than diffing maps, so a
failure names the field.

**Errors are typed, predeclared and actionable.** No `errors.New` or bare
`fmt.Errorf` inside a function: every error is a package-level sentinel or a
typed value that wraps one, so callers branch with `errors.Is` and never
parse a message. Every message states what went wrong and what the reader
should do about it, including the value at fault.

**Readable over clever.** Named types instead of nested anonymous structs.
Exported identifiers carry doc comments; unexported ones carry a comment
whenever the reason for their existence is not obvious from the name. Where
the code works around a daemon quirk, a protocol rule or a platform
difference, the comment says which one.

**Streaming by default.** Prefer `io.Reader` and `io.Writer` over
consolidating bytes in memory. Where a convenience that buffers is useful, it
wraps the streaming form rather than replacing it.

**Standard library first.** The core packages take no third-party
dependency. Anything heavier lives in its own module so consumers opt in.

**Every exported function carries three things**: a runnable `Example` with
an `Output:` comment that works with no Docker daemon, a benchmark, and, where
the input is parsed or decoded, a fuzz target asserting safety properties
rather than specific outputs.

## Documentation

The site you are reading lives in `docs/` and is rebuilt on every push to
`master`. A change that adds or alters behaviour should update the guide that
describes it in the same pull request. [Writing a new page](../reference/writing-docs.md)
has the conventions and the local preview command.

## Pull requests

- One concern per pull request. A refactor and a behaviour change are two
  pull requests.
- The description says what changed and why, and names the issue it closes.
- CI must be green: `gofmt`, `go vet`, the root module's tests, the
  `mobyclient` module's tests, and the documentation build.
- Expect review comments about the style points above. They are applied to
  every module in this repository and its sibling, easymongo.
