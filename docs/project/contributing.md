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

This is the standard a change is reviewed against. The repository keeps the
same text in `AGENTS.md`, which arrives with
[PR #53](https://github.com/TopherGopher/mongotest/pull/53), so that people and
coding agents work to one copy.

**The library**

- **Defaults need no documentation.** `mongod.Start(ctx)`, and every other
  entry point, does the right thing with no options. If a caller has to read
  the docs to make a default call work, the default is wrong.
- **As simple as possible.** Prefer removing code to adding it. Do not write
  an edge case that needs hundreds of lines. If it is likely to happen, open
  an issue for it; if it is not, leave it out.
- **Standard library first.** The core packages take no third-party
  dependency. Anything heavier lives in its own module.
- **Errors are typed and actionable.** Every error is a package-level
  sentinel, or a typed value that matches one, so callers use `errors.Is` and
  never parse a message. The message says what went wrong and what to do.

**Tests**

- **Test first.** Write the test, watch it fail for the reason you expect,
  then write the code. Never delete, skip or weaken a test to get a green run.
- **testify.** `require` for a precondition the rest of the test depends on,
  `assert` for the checks. Give each a short message saying what it protects.
- **Meaningful.** A test states a behaviour a caller relies on. If no caller
  would notice it breaking, it is not worth having.
- **Not brittle.** Assert on behaviour and typed values: `errors.Is`, a
  field, a state. Do not assert on message wording, on formatting, or on an
  order of calls that does not matter. A refactor that keeps behaviour must
  not break a test.
- **Examples and benchmarks where they mean something.** A function a
  caller has to learn how to use gets a runnable `Example` that needs no
  Docker daemon. A function whose speed matters gets a benchmark. A getter
  needs neither. Anything that parses input has a fuzz target.

**Code**

- **Small functions.** If a doc comment needs paragraphs to explain a
  function, the function is doing too much: split it into named helpers.
- **At most two conditions in an `if`.** Give a longer test a name as a
  private function, or use a `switch` when there are several branches.
- **Explain what cannot be factored.** Where code has to be intricate, such
  as a daemon quirk or a platform difference, say why in a brief comment
  beside it.
- **Streaming by default.** Prefer `io.Reader` and `io.Writer` over holding
  bytes in memory.

**Comments and docs**

- **Brief and meaningful.** Say what a thing is for and anything a caller
  would get wrong without being told. One or two sentences is usual.
- **It has to read well.** Plain words, whole sentences, one idea each.
- **Read cold before it ships.** Someone with no context, or a fresh agent
  given only the code, reads the change adversarially: does it flow, and is
  it enough? Fix whatever they trip over.

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
