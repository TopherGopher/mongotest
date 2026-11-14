# Working in this repository

mongotest starts real MongoDB containers for Go tests. This file is the
standard every change is held to, whoever or whatever writes it. `CLAUDE.md`
only imports this file, so there is one copy to keep current.

## How work is planned

Work is planned in GitHub issues and nowhere else. There is no plan file.

- **Every pull request starts from an issue.** The issue says what the
  problem is, why it matters, and what done looks like. The pull request
  names the issue it closes.
- **No issue, no work yet.** If the work has no issue, or the issue no
  longer matches it, spawn the `project-manager` to create or update one
  before writing code.
- **Every issue is read cold before it is filed.** The project-manager sends
  its draft to a newly spawned `tone-reviewer`, answers its questions, and
  only then creates or updates the issue.
- **Findings go where the work is.** A measurement or a decision that
  explains a change belongs in its issue or its pull request, not in a
  comment and not in a separate document.

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
- **Streaming by default.** Prefer `io.Reader` and `io.Writer` over holding
  bytes in memory.

## Documentation

- **Brief and meaningful.** Say what a thing is for and anything a caller
  would get wrong without being told. One or two sentences is usual. No
  history, no restating the code, no measurements: those go in the issue
  or the pull request.
- **It has to read well.** Plain words, whole sentences, one idea each.

## The team

The lead is the session doing the work. It makes every change to code and
docs itself, and brings in teammates for what they are for. Each teammate is
defined in `.claude/agents/`.

| Teammate | Brought in to |
| --- | --- |
| `project-manager` | create or update a GitHub issue; the only one who writes issues |
| `tone-reviewer` | read an issue or a piece of writing cold, and say whether it reads well and is understood |
| `architecture-expert` | judge package boundaries, dependencies and the shape of the public API |
| `golang-expert` | review Go for correctness, concurrency, errors and idiom |
| `mongodb-expert` | say how mongod and its images actually behave |
| `qa-expert` | say what needs a test, and whether a test is meaningful |
| `ux-expert` | judge what the library is like to pick up with no documentation |
| `documentation-expert` | review or draft docs, comments and messages for tone and brevity |
| `security-expert` | review exposure, credentials, CI and dependencies |
| `skeptic` | ask, constructively, why a decision is what it is and whether something simpler would do |
| `decider` | make a decision in the maintainer's place |

- **Start a teammate when it is needed and stop it when its work is done.**
  Do not keep one idle, and do not start one for a question you can answer
  by reading the code.
- **The tone-reviewer is new every time.** Its value is that it knows
  nothing, so never reuse one. It may ask the project-manager for context,
  and suggests wording and missing context; it does not decide scope.
- **Experts advise; the lead changes things.** Only the project-manager
  writes outside the working tree, and only issues.
- **The decider is for one moment only:** when the lead would otherwise stop
  and ask the maintainer a question. Then the decider decides, and the lead
  carries on. It is never asked for a review or an opinion. It runs on a
  Fable model, reads no code and uses no skills: it asks teammates for the
  facts it needs. A choice that is destructive or cannot be undone still
  goes to the maintainer.

## Before a change is done

1. `gofmt -l .` prints nothing, and `go vet ./...` passes.
2. `go test -race ./...` passes here and in `mobyclient/`. The integration
   tests need a Docker daemon, and fail rather than skip without one.
3. A reviewer with no context reads the change adversarially: a newly
   spawned `tone-reviewer` for the writing, and the experts the change
   calls for, each given only the code and docs. Anything exported goes to
   the `ux-expert`, and any test to the `qa-expert`. Fix whatever they trip
   over.
