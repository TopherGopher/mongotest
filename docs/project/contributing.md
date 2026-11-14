---
title: Contributing
description: How to build and test mongotest, and what a change has to meet.
---

# Contributing

Issues and pull requests are welcome at
[github.com/TopherGopher/mongotest](https://github.com/TopherGopher/mongotest).
This page says how to build and test, and what a change has to meet.

## Build and test

```sh
git clone https://github.com/TopherGopher/mongotest
cd mongotest

gofmt -l .                              # must print nothing
go vet ./...
go test -race ./...                     # root module

cd mobyclient && go test -race ./...    # separate module, not reached by ./...
```

Integration tests need a Docker daemon, and pull the images they use. Without
a daemon they fail rather than skip.

Benchmarks and fuzzing:

```sh
go test ./... -run='^$' -bench=. -benchmem
go test ./dockerclient/ -run='^$' -fuzz=FuzzCheckID -fuzztime=30s
```

## Known traps

- **Do not commit a `go.work`.** It breaks the root build. If `go work sync`
  has rewritten `go.mod` and `go.sum`, restore them with
  `git checkout -- go.mod go.sum`.
- **`mobyclient/go.mod` carries a `replace`** pointing at the repository root
  until a tag contains `dockerclient`. It is annotated in the file.
- **The old root implementation** still compiles against
  `github.com/docker/docker v20.10.17`. Do not upgrade that dependency until issue #44.

## The standard

A change is reviewed against `AGENTS.md` at the repository root, which
arrives with [PR #53](https://github.com/TopherGopher/mongotest/pull/53). In
short:

- Every pull request starts from an issue that says what the problem is and
  what done looks like.
- A call with no options does the right thing without the caller reading
  the docs, and the simplest code that works wins.
- The test is written first. It asserts on behaviour and typed errors, never
  on message wording.
- Comments and docs are brief: plain words, whole sentences, no history and
  no measurements.
- Someone with no context reads the change before it merges.

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
