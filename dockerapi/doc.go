// Package dockerapi is a minimal Docker Engine API client built on the Go
// standard library only.
//
// It exists so that mongotest can pull an image, start a container, copy
// files into it, run commands inside it and remove it, without depending on
// github.com/docker/docker or its successors. Only the endpoints mongotest
// needs are implemented: image pull and inspect; container create, start,
// remove, inspect and top; copying an archive into a container; and exec
// with streamed stdout and stderr.
//
// # Getting a client
//
// FromEnv locates the daemon the same way the docker CLI does: the
// DOCKER_HOST environment variable, then the DOCKER_CONTEXT variable or the
// currentContext recorded in the Docker config directory (DOCKER_CONFIG or
// ~/.docker). Configuration always wins; the search below only runs when
// none of it is set.
//
// With nothing configured, FromEnv looks for a daemon on the well-known
// socket paths and uses the first one that answers:
//
//	$XDG_RUNTIME_DIR/docker.sock          rootless Docker
//	/var/run/docker.sock                  system Docker
//	$XDG_RUNTIME_DIR/podman/podman.sock   rootless Podman
//	/run/podman/podman.sock               system Podman
//
// When XDG_RUNTIME_DIR is unset, /run/user/$UID stands in for it, which is
// the same fallback Podman itself applies. On macOS the podman machine
// sockets are searched as well, since their path carries the machine name
// and has moved between releases; the common case is covered earlier,
// because podman machine forwards the API to /var/run/docker.sock.
//
// Podman serves the Docker Engine API on those sockets, so a machine running
// only Podman works with no configuration at all. Docker is preferred when
// both are running, since this is a Docker client. The probe connects rather
// than checking that the file exists, so a socket left behind by a stopped
// daemon does not shadow one that is actually running.
//
// When none of them answers, FromEnv still returns the Docker default and
// the failure surfaces on the first call, as a connection error naming the
// socket and saying what to do. Discovery itself does not fail, so building
// a client never depends on a daemon being up.
//
// On Windows the default named pipe cannot be dialled by this client, so
// Docker Desktop's optional tcp://localhost:2375 endpoint is probed first,
// then the AF_UNIX socket podman machine has exposed under TEMP since Podman
// 5.3. The error explains both when neither answers.
//
// # Podman
//
// Everything in this package works against Podman's Docker-compatible
// endpoint. Two differences are worth knowing.
//
// Podman capped the compatible API at 1.41 from 4.x through 5.7, raising it
// to 1.44 in 5.8. This client asks for PreferredAPIVersion and accepts
// anything down to MinSupportedAPIVersion, so it negotiates downwards and
// works against all of them. A client that refuses to go below its own
// preferred version cannot talk to those releases at all.
//
// Runtime reports which engine answered, and ServerProduct reports how it
// described itself. Version errors name that product, so someone running
// Podman is never told to upgrade a docker daemon they do not have.
//
//	if c.Runtime() == dockerapi.RuntimePodman {
//		// for example: skip a check that only makes sense on Docker
//	}
//
// Podman is identified by the Libpod-API-Version header it sets on every
// response, falling back to the component list in GET /version. Platform.Name
// is not used for this: Podman puts a platform triple there, and a moby
// build from source leaves it empty.
//
//	c, err := dockerapi.FromEnv()
//	if err != nil {
//		return err // the message says which variable or setting to fix
//	}
//
// New accepts options for a fixed host, a custom http.Client, TLS, the API
// version and logging:
//
//	c, err := dockerapi.New(
//		dockerapi.WithHost("tcp://build-host:2376"),
//		dockerapi.WithTLSConfig(tlsCfg),
//		dockerapi.WithLogger(slog.Default()),
//	)
//
// TLS for tcp hosts follows the docker CLI conventions: DOCKER_TLS_VERIFY
// enables it and DOCKER_CERT_PATH (default: the config directory) holds
// ca.pem and, optionally, cert.pem and key.pem.
//
// # API versions
//
// The daemon and the client must agree on an Engine API version, and the
// windows they support differ (Podman's compatible socket tops out at 1.41,
// recent Docker Engines start at 1.40 or later). The first request asks the
// daemon for its window with GET /version and picks the highest version both
// sides support, up to PreferredAPIVersion; every later request carries
// that version in its path. WithAPIVersion, or the DOCKER_API_VERSION
// variable, pins a version and skips the round trip.
//
// # Running a container
//
// A typical sequence: create (pulling the image first if the daemon does not
// have it), copy files in while the container is still stopped, start, wait
// for a port, run a command, remove.
//
//	cfg := dockerapi.ContainerConfig{
//		Image:        "mongo:8",
//		Labels:       map[string]string{"mongotest": "regression"},
//		ExposedPorts: map[string]struct{}{"27017/tcp": {}},
//		HostConfig: &dockerapi.HostConfig{PortBindings: map[string][]dockerapi.PortBinding{
//			"27017/tcp": {{HostIP: "127.0.0.1", HostPort: ""}}, // "" lets the daemon pick a free port
//		}},
//	}
//	id, _, err := c.ContainerCreate(ctx, "", cfg)
//	if dockerapi.IsNotFound(err) {
//		if err = c.ImagePull(ctx, cfg.Image); err != nil {
//			return err
//		}
//		id, _, err = c.ContainerCreate(ctx, "", cfg)
//	}
//	if err != nil {
//		return err
//	}
//	defer c.ContainerRemove(context.Background(), id, dockerapi.RemoveOptions{Force: true, RemoveVolumes: true})
//
//	err = c.CopyToContainer(ctx, id, "/etc", []dockerapi.File{{Name: "mongo-tls/ca.pem", Mode: 0o644, Content: caPEM}})
//	err = c.ContainerStart(ctx, id)
//	info, err := c.ContainerInspect(ctx, id)
//	port := info.HostPort("27017/tcp")
//
//	res, err := c.Exec(ctx, id, "mongosh", "--quiet", "--eval", "db.runCommand({ping:1}).ok")
//	fmt.Println(res.Stdout, res.ExitCode)
//
// # Streaming
//
// Large payloads are streamed rather than buffered. CopyToContainer produces
// the tar archive through a pipe while the request is in flight, and
// CopyArchiveToContainer accepts any io.Reader that yields a tar stream.
// ExecStartTo writes stdout and stderr into caller-provided writers as the
// process produces them; ExecStart and Exec are conveniences that collect
// the output in memory.
//
// # Errors
//
// Every error is either a predeclared sentinel or a typed value that wraps
// one, so callers branch with errors.Is and never parse messages:
//
//   - ErrInvalidArgument (InvalidArgumentError): a value was rejected before
//     any request was sent. The message names the argument, the problem and
//     the fix.
//
//   - ErrConnectionFailed (ConnectionError): the daemon could not be reached.
//     The message says whether the socket is missing, permission was denied
//     or the connection was refused, and what to do about it.
//
//   - ErrAPIVersion (APIVersionError): the version windows do not overlap, or
//     a feature such as ExecConfig.WorkingDir needs a newer daemon.
//
//   - ErrNotFound, ErrConflict, ErrUnauthorized (StatusError): the daemon
//     answered 404, 409, or 401/403. StatusError carries the daemon's own
//     message plus a hint for the status code.
//
//   - ErrDaemonResponse (ResponseError): the daemon answered with a status
//     or body this client could not interpret.
//
//   - ErrStream (StreamError), ErrPull (PullError): an exec stream or a pull
//     progress stream reported a problem.
//
//     if dockerapi.IsNotFound(err) { /* pull, then retry */ }
//     var ia *dockerapi.InvalidArgumentError
//     if errors.As(err, &ia) { fmt.Println(ia.Argument, ia.Fix) }
//
// # Testing code that uses this package
//
// Point the client at a fake daemon with WithHost, or hand it an http.Client
// whose Transport is an in-process http.RoundTripper with WithHTTPClient.
// The test doubles live in github.com/tophergopher/mongotest/dockermock:
// Daemon is an httptest-based daemon that records requests and serves
// configurable routes over a unix socket or TCP, and Mock and Fake stand in
// for a Client without any HTTP at all. Every example in this package runs
// against a dockermock.Daemon, so they execute on the documentation site and
// in "go test" without a Docker daemon installed.
//
// This package's own suite adds benchmarks for each exported call, measured
// against a stubbed daemon so the numbers reflect this client's cost rather
// than the daemon's, and fuzz targets for the parsers and stream decoders.
// The fuzzed properties are safety properties: the parsers never panic, an
// accepted id or reference can never change the shape of a request, and an
// accepted set of files always produces a tar archive that extracts inside
// the destination directory.
package dockerapi
