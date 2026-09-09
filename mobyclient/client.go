package mobyclient

import (
	"net/http"

	"github.com/moby/moby/client"

	"github.com/tophergopher/mongotest/dockerclient"
)

// Client wraps a *client.Client from github.com/moby/moby/client so it
// satisfies dockerclient.Client. It is safe for concurrent use: the moby
// client it wraps is.
type Client struct {
	cli    *client.Client
	host   string
	logger dockerclient.Logger
}

// Client satisfies the interface every mongotest package depends on.
var _ dockerclient.Client = (*Client)(nil)

// Option configures a Client built by New.
type Option func(*clientConfig) error

// clientConfig accumulates the options passed to New before the underlying
// moby client is built.
type clientConfig struct {
	host       string
	mobyClient *client.Client
	logger     dockerclient.Logger
}

// WithHost sets the daemon address (unix:///path, tcp://host:port,
// http(s)://host:port) and skips discovery. It has no effect when combined
// with WithMobyClient, since that client is already fully configured.
func WithHost(host string) Option {
	return func(cfg *clientConfig) error {
		cfg.host = host
		return nil
	}
}

// WithMobyClient wraps a *client.Client the caller already constructed,
// instead of building one from WithHost. Use this when the caller needs
// moby client options this package does not expose directly, such as a
// custom *tls.Config or extra HTTP headers.
func WithMobyClient(cli *client.Client) Option {
	return func(cfg *clientConfig) error {
		if cli == nil {
			return dockerclient.InvalidArgument("mobyclient client", "", "the value is nil",
				"pass a client built with client.New, or omit WithMobyClient to let mobyclient build one")
		}
		cfg.mobyClient = cli
		return nil
	}
}

// WithLogger sets the logger that receives one Debug record per request,
// via the moby client's response hooks. *slog.Logger satisfies Logger
// directly. The default discards everything. It has no effect when combined
// with WithMobyClient, since hooks are only attached to a client this
// package builds.
func WithLogger(l dockerclient.Logger) Option {
	return func(cfg *clientConfig) error {
		if l != nil {
			cfg.logger = l
		}
		return nil
	}
}

// New creates a Client. When WithHost is not given the underlying moby
// client uses its own default (the platform's local socket); use FromEnv to
// discover the daemon the way the docker CLI does instead.
func New(opts ...Option) (*Client, error) {
	cfg := &clientConfig{logger: dockerclient.NopLogger()}
	for _, opt := range opts {
		if opt == nil {
			continue
		}
		if err := opt(cfg); err != nil {
			return nil, err
		}
	}
	if cfg.mobyClient != nil {
		return &Client{cli: cfg.mobyClient, host: cfg.mobyClient.DaemonHost(), logger: cfg.logger}, nil
	}
	var mopts []client.Opt
	if cfg.host != "" {
		mopts = append(mopts, client.WithHost(cfg.host))
	}
	mopts = append(mopts, client.WithResponseHook(loggingHook(cfg.logger)))
	cli, err := client.New(mopts...)
	if err != nil {
		return nil, wrapConstructError(cfg.host, err)
	}
	return &Client{cli: cli, host: cli.DaemonHost(), logger: cfg.logger}, nil
}

// FromEnv locates the daemon exactly as the docker CLI and moby client do:
// DOCKER_HOST, DOCKER_API_VERSION, and DOCKER_CERT_PATH/DOCKER_TLS_VERIFY
// for TLS. It is New with the moby client's own FromEnv option; use New with
// WithLogger for request logging.
func FromEnv() (*Client, error) {
	cli, err := client.New(client.FromEnv)
	if err != nil {
		return nil, wrapConstructError("", err)
	}
	return &Client{cli: cli, host: cli.DaemonHost(), logger: dockerclient.NopLogger()}, nil
}

// loggingHook builds a client.ResponseHook that logs one Debug record per
// request, in the same shape dockerapi's WithLogger produces, so a caller
// switching clients sees familiar log lines.
func loggingHook(logger dockerclient.Logger) client.ResponseHook {
	return func(resp *http.Response) {
		if resp == nil || resp.Request == nil {
			return
		}
		logger.Debug("docker request", "method", resp.Request.Method, "path", resp.Request.URL.Path, "status", resp.StatusCode)
	}
}

// wrapConstructError turns a failure to build the underlying moby client
// (an unparsable host string, usually) into an InvalidArgumentError, since
// nothing has been sent to a daemon yet.
func wrapConstructError(host string, err error) error {
	if err == nil {
		return nil
	}
	return dockerclient.InvalidArgument("docker host", host, err.Error(),
		`use a host like "unix:///var/run/docker.sock" or "tcp://host:2375"`)
}

// Host returns the daemon address the client resolved, in DOCKER_HOST form.
func (c *Client) Host() string { return c.host }

// mapErr turns an error the moby client returned for one request into a
// dockerclient sentinel or carrier; see errors.go.
func (c *Client) mapErr(method, path string, err error) error {
	return mapError(c.host, method, path, err)
}
