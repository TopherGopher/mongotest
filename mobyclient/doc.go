// Package mobyclient wraps the official Docker client,
// github.com/moby/moby/client, so it satisfies
// github.com/tophergopher/mongotest/dockerclient.Client. It is its own Go
// module so a caller who does not need it never pulls in the moby client's
// dependency tree (containerd/errdefs, opentelemetry, distribution/reference
// and the rest).
//
// # Choosing between mobyclient and dockerapi
//
// dockerapi, the standard-library client in the parent module, is the
// default: it takes no third-party dependency and covers everything
// mongotest needs. Reach for mobyclient instead when the process already
// depends on github.com/moby/moby/client for other reasons (so this adds
// nothing new to the build), or when a caller needs a daemon endpoint
// dockerapi does not implement and wants to keep using the moby client's own
// richer surface (options, streaming responses, the full Engine API) for
// that, while still handing mongotest a Client it understands.
//
// # Getting a client
//
// FromEnv locates the daemon the same way the docker CLI does: DOCKER_HOST,
// then DOCKER_API_VERSION, DOCKER_CERT_PATH and DOCKER_TLS_VERIFY for TLS.
// It is github.com/moby/moby/client's own FromEnv option, so behaviour
// matches the moby-based docker CLI exactly:
//
//	c, err := mobyclient.FromEnv()
//	if err != nil {
//		return err
//	}
//
// New accepts options for a fixed host, an already-constructed moby client,
// and logging:
//
//	c, err := mobyclient.New(
//		mobyclient.WithHost("tcp://build-host:2376"),
//		mobyclient.WithLogger(slog.Default()),
//	)
//
// WithMobyClient wraps a *client.Client the caller already built (with its
// own TLS, headers or transport configured), rather than constructing one:
//
//	moby, err := client.New(client.WithHost("tcp://build-host:2376"))
//	c, err := mobyclient.New(mobyclient.WithMobyClient(moby))
//
// # Errors
//
// Every error mobyclient returns is one of the shared dockerclient
// sentinels or a typed value that wraps one, exactly as dockerapi's are, so
// calling code branches with errors.Is without caring which client produced
// the failure:
//
//	if dockerclient.IsNotFound(err) { /* pull, then retry */ }
//
// mobyclient maps the moby client's own errors (github.com/moby/moby/client
// classifies failures with github.com/containerd/errdefs) onto
// dockerclient's StatusError, ConnectionError, ResponseError, StreamError
// and PullError, preserving the daemon's own message. Input is validated
// with the same dockerclient helpers dockerapi uses, so a bad image
// reference, container id or port binding is rejected with the same message
// before any request is sent.
//
// # Testing code that uses this package
//
// mobyclient's own suite runs the shared conformance and benchmark tests
// from dockerclient/dockerclienttest against a client pointed at
// dockermock.Daemon, so it is held to the same behaviour as dockerapi and
// the doubles in dockermock. Every exported function has a runnable Example
// backed by the same fake daemon, so they execute with no Docker daemon
// installed.
package mobyclient
