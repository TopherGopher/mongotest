package mongod

import (
	"context"
	"net"
	"path"
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

// mongodProgram is the program whose presence in the container's process
// listing means the server itself is running, as opposed to the entrypoint
// script that will eventually exec it.
const mongodProgram = "mongod"

// commandTitles are the names the daemon gives the command column of a
// process listing. Which one appears depends on the ps arguments the daemon
// was asked for, so the column is found by name rather than by position.
var commandTitles = []string{"CMD", "COMMAND", "ARGS"}

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

// wait returns once mongod is running inside the container and the address
// the caller was given accepts a connection.
//
// Neither half is sufficient alone. Docker's userland proxy binds the
// published port as soon as the container is created, before the entrypoint
// has run, so a dial succeeds against a container whose only process is runc
// init: that cost a CI failure once already. Equally, a process listing says
// nothing about whether the port a caller was handed reaches it, which is the
// thing that breaks in every containerised CI shape.
//
// What this does not prove is that mongod is accepting MongoDB connections.
// The proxy accepts on its behalf, so between mongod being exec'd and it
// listening on 27017 there is a window this cannot see into. Proving that
// takes a MongoDB conversation, which needs a driver; the driver layers above
// this package ping with backoff for exactly that reason. What is guaranteed
// here is narrower and still worth having: the server process exists, the
// container has not died, and the address in the URI is live.
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
			// A container that has not started yet answers 409, which is a
			// normal moment during a start. One that crashed answers the same
			// 409 forever, and one that was removed answers 404, so the two
			// are told apart by asking what state the container is in.
			if exited := p.exitedError(ctx, err); exited != nil {
				return exited
			}
			lastErr = err
		case running:
			if err := p.dial(ctx); err == nil {
				p.logger.Debug("mongod is running and the published port answers",
					"container", p.name, "address", addr(p.host, p.port))
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

// mongodIsRunning reports whether mongod itself is in the container's process
// listing.
//
// Only the command column is read, and only its first token. The mongo image
// starts a shell script that takes mongod as an argument
// ("bash /usr/local/bin/docker-entrypoint.sh mongod") and runs as a user
// called mongodb, so a search for the text "mongod" anywhere in the listing
// matches from the first instant of a start, long before the server exists.
func (p readyProbe) mongodIsRunning(ctx context.Context) (bool, error) {
	top, err := p.docker.ContainerTop(ctx, p.id)
	if err != nil {
		return false, err
	}
	column := commandColumn(top.Titles)
	for _, process := range top.Processes {
		if column >= len(process) {
			continue
		}
		if isMongod(process[column]) {
			return true, nil
		}
	}
	return false, nil
}

// commandColumn returns the index of the command column. The command is
// conventionally last in ps output, which is the fallback when the daemon
// reports titles this does not recognise.
func commandColumn(titles []string) int {
	for i, title := range titles {
		for _, candidate := range commandTitles {
			if strings.EqualFold(strings.TrimSpace(title), candidate) {
				return i
			}
		}
	}
	return max(len(titles)-1, 0)
}

// isMongod reports whether a command line is mongod itself. The program may
// be given by path, so it is the base name that is compared.
func isMongod(command string) bool {
	program, _, _ := strings.Cut(strings.TrimSpace(command), " ")
	if program == "" {
		return false
	}
	return path.Base(program) == mongodProgram
}

// exitedError returns an error when the container is past the point of ever
// becoming ready, and nil when it is merely not ready yet.
//
// Without this a container that crashed on startup, which answers the same
// 409 as one that has not started, is polled until the whole budget is spent
// and then reported as a timeout: the wrong diagnosis, an hour of CI time,
// and an exit code nobody is shown.
func (p readyProbe) exitedError(ctx context.Context, cause error) error {
	info, err := p.docker.ContainerInspect(ctx, p.id)
	if dockerclient.IsNotFound(err) {
		// Something removed the container underneath us. There is nothing
		// left to wait for.
		return &ContainerExitedError{Name: p.name, ID: p.id, Status: "removed", Err: cause}
	}
	if err != nil {
		// The inspect failed for some other reason, so nothing is known about
		// the container's state and the wait continues.
		return nil
	}
	if info.State.Running {
		return nil
	}
	switch info.State.Status {
	case "exited", "dead":
		return &ContainerExitedError{
			Name: p.name, ID: p.id, Status: info.State.Status,
			ExitCode: info.State.ExitCode, HasExitCode: true, Err: cause,
		}
	default:
		// created, restarting, paused, removing: all still moving.
		return nil
	}
}

// dial opens and closes a connection to the published port.
func (p readyProbe) dial(ctx context.Context) error {
	dialer := net.Dialer{Timeout: dialTimeout}
	conn, err := dialer.DialContext(ctx, "tcp", addr(p.host, p.port))
	if err != nil {
		return err
	}
	return conn.Close()
}
