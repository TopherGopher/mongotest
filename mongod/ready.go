package mongod

import (
	"context"
	"net"
	"strconv"
	"strings"
	"time"

	"github.com/tophergopher/mongotest/dockerclient"
)

// Polling for readiness. The interval grows so that a container that comes up
// in milliseconds is noticed at once, while one that takes the better part of
// a minute does not cost the daemon a thousand requests.
const (
	firstPollInterval = 25 * time.Millisecond
	maxPollInterval   = 500 * time.Millisecond
	dialTimeout       = 2 * time.Second
)

// readyProbe waits for a started container to be usable.
type readyProbe struct {
	docker dockerclient.Client
	logger dockerclient.Logger
	// id and name identify the container, for the daemon and for a reader.
	id   string
	name string
	// host and port are the address a caller will connect to.
	host string
	port int
	// timeout bounds the whole wait.
	timeout time.Duration
}

// wait returns once mongod is running inside the container and accepting
// connections on the published port.
//
// Both halves are needed. Docker's userland proxy binds the host port as soon
// as the container is created, before the entrypoint has run, so a dial
// succeeds against a container whose only process is runc init: that cost a
// CI failure once already. The process listing is what says mongod is really
// there, and the dial is what says the address the caller was handed actually
// reaches it.
func (p readyProbe) wait(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, p.timeout)
	defer cancel()

	interval := firstPollInterval
	timer := time.NewTimer(interval)
	defer timer.Stop()

	var lastErr error
	for {
		running, err := p.mongodIsRunning(ctx)
		switch {
		case err != nil:
			// A container that has not started yet answers 409, which is the
			// normal state during a start rather than something to report.
			lastErr = err
		case running:
			if err := p.dial(ctx); err == nil {
				p.logger.Debug("mongod is ready", "container", p.name, "address", addr(p.host, p.port))
				return nil
			} else {
				lastErr = err
			}
		}

		select {
		case <-ctx.Done():
			return &NotReadyError{
				Name: p.name, ID: p.id, Host: p.host, Port: p.port,
				Timeout: p.timeout, Cause: ctx.Err(), LastErr: lastErr,
			}
		case <-timer.C:
			interval = min(interval*2, maxPollInterval)
			timer.Reset(interval)
		}
	}
}

// mongodIsRunning reports whether a mongod process is in the container's
// process listing.
func (p readyProbe) mongodIsRunning(ctx context.Context) (bool, error) {
	top, err := p.docker.ContainerTop(ctx, p.id)
	if err != nil {
		return false, err
	}
	for _, process := range top.Processes {
		for _, field := range process {
			if strings.Contains(field, "mongod") {
				return true, nil
			}
		}
	}
	return false, nil
}

// dial opens and closes a connection to the published port.
func (p readyProbe) dial(ctx context.Context) error {
	dialer := net.Dialer{Timeout: dialTimeout}
	conn, err := dialer.DialContext(ctx, "tcp", net.JoinHostPort(p.host, strconv.Itoa(p.port)))
	if err != nil {
		return err
	}
	return conn.Close()
}
