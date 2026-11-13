# Working in this repository

mongotest starts real MongoDB containers for Go tests. This file is the
standard every change is held to. `PLAN.md` holds the roadmap and the
measurements behind the design.

## The library

- **Defaults need no documentation.** `mongod.Start(ctx)`, and every other
  entry point, does the right thing with no options. If a caller has to read
  the docs to make a default call work, the default is wrong.
- **As simple as possible.** Prefer removing code to adding it. Do not write
  an edge case that needs hundreds of lines. If it is likely to happen, open a
  GitHub issue for it; if it is not, leave it out.
- **Standard library first.** The core packages take no third-party
  dependency. Anything heavier lives in its own module.
- **Errors are typed and actionable.** Every error is a package-level
  sentinel, or a typed value that matches one, so callers use `errors.Is` and
  never parse a message. The message says what went wrong and what to do.

## Tests

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
- Every exported function has a runnable `Example` that needs no Docker
  daemon, and a benchmark. Anything that parses input also has a fuzz target.

## Code

- **Small functions.** If a doc comment needs paragraphs to explain a
  function, the function is doing too much: split it into named helpers.
- **At most two conditions in an `if`.** Give a longer test a name as a
  private function, or use a `switch` when there are several branches.
- **Explain what cannot be factored.** Where code has to be intricate, such
  as a daemon quirk or a platform difference, say why in a brief comment
  beside it.

## Documentation

- **Brief and meaningful.** Say what a thing is for and anything a caller
  would get wrong without being told. One or two sentences is usual. No
  history, no restating the code, no measurements: those go in `PLAN.md`.
- **It has to read well.** Plain words, whole sentences, one idea each.

## Before a change is done

1. `gofmt -l .` prints nothing, and `go vet ./...` passes.
2. `go test -race ./...` passes here and in `mobyclient/`. The integration
   tests need a Docker daemon, and fail rather than skip without one.
3. A reviewer with no context reads the change adversarially: a fresh agent
   given only the code and docs, asked whether it flows and whether it is
   enough. Fix whatever it trips over.
