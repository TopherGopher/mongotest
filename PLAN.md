# mongotest refactor plan

Status: agreed design, implementation not started. Companion plan for the
sibling repo lives in `easymongo/PLAN.md`.

## Goals

1. Remove the easymongo dependency from mongotest. mongotest depends only on
   the official MongoDB Go driver; easymongo depends on mongotest (one-way).
2. Support driver v1 and driver v2 using the driver's own convention:
   `github.com/tophergopher/mongotest` (driver v1) and
   `github.com/tophergopher/mongotest/v2` (driver v2, nested module in `v2/`).
3. Radically lighten dependencies. Talk to the Docker Engine API with the Go
   standard library only. No `github.com/docker/docker`, no `moby`, no
   logrus, no mongo-tools in the core modules.
4. Latest Go (`go 1.27`), latest GitHub Actions, latest packages.
5. Test-driven: every behaviour below gets a failing test first.

## Decisions taken (with the user)

| Topic | Decision |
|---|---|
| Docker transport | Hand-rolled `net/http` client over unix socket / tcp with `GET /version` API negotiation |
| v1 / v2 layout | Root module = driver v1; `v2/` nested module = driver v2; driver-free core packages in the root module shared by both |
| Public API | Redesign (`Start`/`Run` + functional options) plus the old names kept as plain convenience wrappers, not marked deprecated |
| Features kept | Replica set mode, exec / run script in container (via `mongosh`), TLS mode (finished properly) |
| Database exporter | Moved to `exporter/` as its own module with its own go.mod (it is the only place mongo-tools is allowed) |
| Go version | `go 1.27` in every module; CI on latest 1.27.x |
| Client abstraction | `dockerclient` interface package owns the shared types and error sentinels; `dockerapi` is the default implementation, `mobyclient` an opt-in module, `dockermock` the shared test doubles |
| Encoding | `encoding/json/v2`, standard in Go 1.27 with no experiment flag |
| Logging | `log/slog` by default; constructor accepts a small `Logger` interface; logrus, zap and zerolog adapters as isolated sub-modules with their own go.mod and tests |
| Default image | `mongo:8`, overridable by option and by `MONGOTEST_IMAGE` |
| No Docker daemon | Integration tests fail hard (no skipping) |


## Client architecture (agreed after the dockerapi work)

Callers depend on an interface, not on a concrete Docker client, so the
transport is a plug-in choice:

```
dockerclient   the interface, the shared types and the error sentinels.
               No dependencies. Everything else points at this.
   |
   +-- dockerapi    standard-library implementation (the default)
   +-- mobyclient   own module, wraps github.com/moby/moby/client
   +-- dockermock   test doubles: Mock, Fake and an HTTP-level Daemon
```

- `dockerclient` owns `ContainerConfig`, `ExecResult`, `File`, `Logger` and
  every error sentinel and carrier. Both implementations construct the same
  errors, so `errors.Is(err, dockerclient.ErrNotFound)` behaves identically
  whichever client is plugged in.
- `dockerapi` keeps its current type names as aliases of the `dockerclient`
  types, so nothing that already compiles against it breaks.
- `mobyclient` is a separate module: consumers who want the official client
  opt into its dependency tree, and the root module stays standard-library
  only.
- `dockermock` is the single home for test doubles. `Mock` has one function
  field per method and records calls, for tests that assert an exact
  sequence. `Fake` keeps an in-memory container store, for tests that just
  need a working daemon. `Daemon` is the HTTP-level fake (promoted from
  `internal/fakedaemon`) that a real `dockerapi` or `mobyclient` can be
  pointed at, which is what the examples and the wire-level tests use.
- `dockerclient/dockerclienttest` holds one conformance suite and one
  benchmark suite that every implementation runs, so the three clients are
  held to the same behaviour and measured on the same scale.
- `mongod` and everything above it take a `dockerclient.Client`, never a
  concrete type.

## Findings that shape the design

- Baseline against a current daemon (Docker 29.3.1 / `mongo:8`): the container
  starts but the code shells out to the legacy `mongo` binary, which does not
  exist in `mongo:6+` images. Only `mongosh` is present.
- Replica set init hardcodes `_id: 'cf'` regardless of the requested name.
- TLS mode mounts the CA certificate as mongod's `--sslPEMKeyFile`; mongod
  needs a server certificate plus private key there. Bind mounts also break
  with remote daemons. Fix: generate CA + server cert, copy them into the
  created-but-not-started container via the archive endpoint, then start.
- The `init()` signal handler registers `signal.Notify` for SIGINT/SIGTERM in
  every binary that imports the package and never re-raises, so Ctrl-C stops
  terminating the process. Fix: install lazily on first container start,
  reap, then `signal.Reset` and re-raise.
- API version negotiation is mandatory: Podman's Docker socket tops out at
  1.41, Engine 29 negotiates 1.40 to 1.54 here and is stricter elsewhere.
- Measured footprints (modules in graph / go.sum lines): today 95 / 253;
  `moby/moby/client` 49 / 41; stdlib client 0 / 0; driver v2 + testify 15 / 4.
- mongo-driver v2.9.0 requires Go 1.25.0; latest stable Go is 1.27.1.
- Podman serves the Docker Engine API, so this client works against it
  unchanged once it is pointed at the right socket. Discovery now finds that
  socket by itself: with nothing configured it probes rootless Docker, system
  Docker, rootless Podman and system Podman in that order and takes the first
  that answers. `/run/user/$UID` stands in when `XDG_RUNTIME_DIR` is unset,
  which is Podman's own fallback. macOS adds the podman machine sockets,
  globbed because the path carries the machine name and moved at 5.0.
  Windows finds the AF_UNIX socket podman machine has exposed under `TEMP`
  since 5.3, which is dialable where its named pipe is not.
  The probe connects rather than calling `Stat`, because a socket file left
  by a stopped daemon still stats successfully and would shadow a running
  one. It runs only after `DOCKER_HOST` and the docker context, never
  before: configuration is the user saying where the daemon is. Finding
  nothing is not an error; the Docker default is returned and the existing
  connection error surfaces at dial time.
- Podman capped the Docker-compatible API at 1.41 from 4.x through 5.7 and
  raised it to 1.44 in 5.8. `dockerapi` asks for 1.44 and accepts anything
  down to 1.24, so it negotiates downwards and works against all of them.
  This is why some Docker clients cannot talk to Podman at all: they refuse
  anything below their own preferred version. A test fails if the floor is
  ever raised above Podman's cap.
- The daemon is identified by the `Libpod-API-Version` response header, which
  only Podman sets, falling back to the `Components` list in `GET /version`.
  `Platform.Name` is not usable for this: Podman puts a platform triple
  there, and a moby build from source leaves it empty. `Client.Runtime` and
  `Client.ServerProduct` expose the result, and `APIVersionError` names the
  product so a Podman user is never told to upgrade docker.
- Measured `dockerapi` against `mobyclient` on the shared benchmark suite
  (identical benchmark bodies, same `dockermock.Daemon`, median of 5 runs on
  a 4-core Xeon at 2.80GHz). `mobyclient` does 1.35x the allocations and
  1.17x the bytes across the shared cases. The gap is widest where type
  translation costs most: `ContainerCreate` 124us against 210us, and
  `ExecStartTo` 26 KB/op against 75 KB/op. Footprint is the larger
  difference: linking `dockerapi` pulls 0 third-party modules and 208
  packages, `mobyclient` 18 modules and 271 packages, which is 4 go.sum
  lines against 57 in a consuming module, and 10.9 MB against 11.7 MB for a
  trivial binary. These are the baseline numbers a regression is measured
  against; automating the comparison in CI is issue #50.
- `go.work` cannot be committed until the legacy root implementation is gone.
  A workspace resolves one version of each dependency across every member,
  and `github.com/docker/go-connections` is wanted at v0.4.0 by the old
  `docker@v20.10.17` client and at v0.7.0 by `moby/moby/client`. v0.7.0
  dropped `sockets.DialPipe`, which the old client calls, so the root module
  stops compiling. `go work sync` additionally rewrites the root `go.mod` and
  `go.sum`, which breaks the build even after the workspace file is removed.
  Order is therefore: delete the old implementation first, add the workspace
  second. Until then `mobyclient` is built and tested on its own.

## Coding style

These apply to every module in both repositories. They are the standard a
change is reviewed against, not aspirations.

**Test-driven.** Write the test first, run it, confirm it fails for the
reason you expect, then implement until it passes. A test is never deleted,
skipped or weakened to get a green run. When a test must change because the
public API changed, only the types change, never the assertion.

**testify, with a message on every assertion.** `require` for preconditions
that make the rest of the test meaningless, `assert` for the checks
themselves. Every call carries a final message saying what the expectation
protects, in words a reader who did not write the test can act on: not
"expected true" but "the archive is streamed, so no Content-Length is known
up front". Compare typed values field by field rather than diffing maps, so
a failure names the field.

**Errors are typed, predeclared and actionable.** No `errors.New` or bare
`fmt.Errorf` inside a function: every error is a package-level sentinel or a
typed value that wraps one, so callers branch with `errors.Is` and never
parse a message. Every message states what went wrong and what the reader
should do about it, including the value at fault. Fixed-message cases are
predeclared values so they can be compared directly.

**Readable over clever.** Named types instead of nested anonymous structs.
Exported identifiers carry doc comments; unexported ones carry a comment
whenever the reason for their existence is not obvious from the name. Where
the code works around a daemon quirk, a protocol rule or a platform
difference, the comment says which one.

**Streaming by default.** Prefer `io.Reader` and `io.Writer` over
consolidating bytes in memory. Where a convenience that buffers is useful,
it wraps the streaming form rather than replacing it.

**Standard library first.** The core packages take no third-party
dependency. Anything heavier lives in its own module so consumers opt in.

**Every exported function carries three things**: a runnable `Example` with
an `Output:` comment that works with no Docker daemon, a benchmark, and,
where the input is parsed or decoded, a fuzz target asserting safety
properties rather than specific outputs.

## Target layout

```
mongotest/                          module github.com/tophergopher/mongotest   (driver v1)
  go.mod                            go 1.27; mongo-driver v1.17.x; testify (tests only)
  go.work                           local workspace over every module below (only after the
                                    legacy root implementation is deleted; see Findings)
  dockerclient/                     the interface every caller depends on
    client.go                       Client interface; shared types; Logger
    errors.go                       sentinels + typed carriers, shared by all implementations
    validate.go                     argument checks every implementation applies identically
    dockerclienttest/               conformance and benchmark suites any implementation runs
  dockermock/                       test doubles, the only home for them
    mock.go                         Mock: one func field per method, records calls
    fake.go                         Fake: in-memory container store, no scripting needed
    daemon.go                       Daemon: HTTP-level fake a real client can be pointed at
  dockerapi/                        zero-dependency Docker Engine API client (the default)
    client.go                       New(...Option) / FromEnv(); host + context discovery; negotiation
    transport.go                    unix, tcp, tcp+tls (DOCKER_CERT_PATH / DOCKER_TLS_VERIFY)
    version.go                      GET /version, pick min(preferred, server) >= server minimum
    images.go                       ImagePull (drain progress stream, surface error lines), ImageInspect
    containers.go                   ContainerCreate/Start/Remove/Inspect/Top
    archive.go                      CopyToContainer (tar built with archive/tar)
    exec.go                         ExecCreate, ExecStart (hijacked conn), ExecInspect, stdout/stderr demux
    errors.go                       builds the dockerclient error types from HTTP responses
  mobyclient/                       module github.com/tophergopher/mongotest/mobyclient
    client.go                       wraps github.com/moby/moby/client; options; Host
    errors.go                       maps the moby client's errors onto the dockerclient sentinels
    images.go, containers.go        the interface methods, translating types both ways
    archive.go, exec.go             copy and exec, reusing the moby helpers where they exist
    conformance_test.go             runs dockerclienttest.Conformance and .Benchmarks, same as dockerapi
  mongod/                           driver-free container lifecycle
    container.go                    Start(ctx, ...Option) (*Container, error); URI(); Stop(); ID(); Port()
    options.go                      WithImage, WithReplicaSet, WithTLS, WithPort, WithLogger, WithDocker,
                                    WithStartTimeout, WithLabel, WithMongodArgs, WithReuseExisting? (no)
    ready.go                        TCP readiness probe (driver layers do the real ping)
    exec.go                         Exec(ctx, cmd...) (stdout, stderr, exitCode, err); RunScript(ctx, js)
    tls.go                          CA + server cert generation (SANs 127.0.0.1, localhost); TLSConfig()
    reaper.go                       registry of live containers; lazy signal handler; ReapRunningContainers()
  logging.go                        Logger interface, slog adapter, discard default
  mongotest.go                      driver v1 glue: Start/Run/Attach, Instance, replSetInitiate, ping loop
  compat.go                         convenience wrappers: NewTestConnection, NewReplicaSetContainer,
                                    MongoClientWithContainer, TestConnection, KillMongoContainer,
                                    MongoContainerID, RunMongoScriptOnContainer, ExecCommandInMongoContainer,
                                    GetAvailablePort
  v2/                               module github.com/tophergopher/mongotest/v2   (driver v2)
    go.mod                          requires root module (for dockerapi, mongod) + mongo-driver/v2
    mongotest.go, compat.go         same API as root on driver v2
  exporter/                         module github.com/tophergopher/mongotest/exporter (mongo-tools)
  log/logrus/                       module github.com/tophergopher/mongotest/log/logrus
  log/zap/                          module github.com/tophergopher/mongotest/log/zap
  log/zerolog/                      module github.com/tophergopher/mongotest/log/zerolog
  .github/workflows/go.yml          build + vet + gofmt + test (with coverage) across the workspace
  .github/dependabot.yml            gomod for every module dir + github-actions
  Makefile                          tag helper for all module paths (vX, v2/vX, exporter/vX, log/*/vX)
```

`dockerclient`, `dockermock`, `dockerapi` and `mongod` are public packages:
the v2 module must import them, and a zero-dependency Engine client is
useful on its own. `mongod` accepts a `dockerclient.Client`, so a caller can
supply `dockerapi` (the default), `mobyclient`, or a double from
`dockermock` without changing anything above it.

## Public API

Identical in the root package (driver v1 types) and in `v2` (driver v2 types).

```go
// Primary API
func Start(ctx context.Context, opts ...Option) (*Instance, error)
func Run(tb testing.TB, opts ...Option) *Instance      // tb.Fatal on error, tb.Cleanup(Stop)
func Attach(ctx context.Context, uri string) (*Instance, error) // no container, existing server

type Instance struct {
    *mongo.Client                 // promoted: inst.Database("x").Collection("y")
    Container *mongod.Container   // nil for Attach
}
func (i *Instance) URI() string
func (i *Instance) Stop(ctx context.Context) error   // disconnect, remove container, delete temp files; idempotent
func (i *Instance) Exec(ctx context.Context, cmd ...string) (mongod.ExecResult, error)
func (i *Instance) RunScript(ctx context.Context, js string) (string, error) // mongosh --quiet --eval

// Options (re-exported from mongod so callers import one package)
WithImage("mongo:8.0")  WithReplicaSet("rs0")  WithTLS()  WithPort(27018)
WithLogger(l Logger)    WithDocker(*dockerapi.Client)   WithStartTimeout(d)
WithLabel(k, v)         WithMongodArgs("--setParameter", "...")

// Convenience wrappers (kept, plain wrappers over the above)
func NewTestConnection(spinupDockerContainer bool) (*TestConnection, error)
func NewReplicaSetContainer(rsName string) (*TestConnection, error)
func MongoClientWithContainer(f func(*mongo.Client) error) error
type TestConnection = Instance
func (i *Instance) KillMongoContainer() error
func (i *Instance) MongoContainerID() string
func (i *Instance) RunMongoScriptOnContainer(js string) (string, error)
func (i *Instance) ExecCommandInMongoContainer(cmd []string) (string, error)
func GetAvailablePort() (int, error)
```

Logging:

```go
// Satisfied directly by *slog.Logger.
type Logger interface {
    Debug(msg string, keysAndValues ...any)
    Info(msg string, keysAndValues ...any)
    Warn(msg string, keysAndValues ...any)
    Error(msg string, keysAndValues ...any)
}
```
Default is `slog.New(slog.DiscardHandler)`. Adapter modules expose
`logrusadapter.New(*logrus.Entry)`, `zapadapter.New(*zap.Logger)`,
`zerologadapter.New(zerolog.Logger)`, each returning a `mongotest.Logger`.

Behaviours:

- Replica set: container runs `mongod --replSet <name>`; the driver layer
  runs `replSetInitiate` with the requested name and member
  `localhost:27017`, then polls `hello` until `isWritablePrimary`.
- TLS: create container, copy `server.pem` and `ca.pem` into
  `/etc/mongo-tls/`, start with `--tlsMode requireTLS
  --tlsCertificateKeyFile /etc/mongo-tls/server.pem`. `Instance` connects
  with `RootCAs` set to the generated CA; `Container.TLSConfig()` and
  `Container.CAPEM()` are exposed for callers. `RunScript` adds
  `--tls --tlsCAFile` automatically in TLS mode.
- Readiness: `mongod` waits for TCP accept on the published port; the driver
  layer pings with backoff up to `WithStartTimeout` (default 60s).
- Reaper: every started container is registered; the signal handler is
  installed on the first start, removes registered containers, resets the
  signal and re-raises it. `Stop` unregisters. The finalizer is dropped.
- Image pull: `ContainerCreate` first; on 404 pull once and retry.
  Platform is not pinned (official image is multi-arch).
- Docker host discovery: `DOCKER_HOST`, then `DOCKER_CONTEXT` or
  `currentContext` in `~/.docker/config.json` resolved through
  `~/.docker/contexts/meta/<sha256(name)>/meta.json`, then
  `unix:///var/run/docker.sock`. Windows named pipes are out of scope for
  this pass (users can enable the TCP endpoint); see open items.

## TDD sequence

Each step: write the tests, watch them fail, implement, go green, commit.

1. `dockerapi` unit tests against an `httptest` fake daemon (unix socket and
   tcp listeners): host parsing for every `DOCKER_HOST` form and context file;
   negotiation (server max below/above preferred, server minimum above ours
   errors clearly); request shape for each endpoint (method, path, query,
   headers, JSON body); error mapping (404 to `ErrNotFound`, JSON `message`
   surfaced); pull stream draining including an `error` line; archive tar
   contents; exec hijack demux with frames split across reads and exit code.
2. `dockerapi` integration tests against the real daemon using `mongo:8`:
   pull, create, start, top, exec, copy, remove, remove-again is NotFound.
3. `mongod` tests: defaults (`mongo:8`, env override), option application
   into container config (labels, cmd for replSet/TLS/extra args, port
   binding on 127.0.0.1), readiness, `Stop` idempotence, reaper registry,
   TLS material verifies for 127.0.0.1 and localhost, exec output.
4. Root package (driver v1): `Run(t)` insert/find; `Attach`; replica set
   (`rs.status().ok == 1`, a transaction commits); TLS (CA client connects,
   plain client fails); each convenience wrapper; example tests restored.
5. `v2/` package: the same suite on driver v2.
6. Log adapters: each adapter proves message, level and key/values reach the
   underlying logger (logrus test hook, zap observer, zerolog buffer).
7. `exporter/`: JSON and CSV export from a running instance; gzip option.
8. Toolchain (last, after the module graph has shrunk): `go 1.27` everywhere,
   `go mod tidy`, remove `replace` lines, CI and dependabot updates, README.

## Open items with proposed defaults

- Windows named pipe support needs `go-winio` (windows-only compile, but still
  in go.sum). Default: not in this pass; document TCP endpoint.
- mongo-tools version and its driver line for `exporter/`: verify at
  implementation; it only needs a URI so the API stays driver-neutral.
- Exact GitHub Actions tags (`checkout`, `setup-go`, `coverallsapp`,
  `gcov2lcov`) will be confirmed at implementation time.
- Coverage across a multi-module workspace: merge profiles before Coveralls.
- Optional: add golangci-lint to CI (not currently present).
