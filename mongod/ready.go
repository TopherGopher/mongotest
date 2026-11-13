package mongod

import (
	"context"
	"errors"
	"net"
	"path"
	"strings"
	"time"

	"github.com/tophergopher/mongotest/dockerclient"
)

// Readiness is polled. The interval doubles from the first to the maximum,
// so a quick start is noticed at once and a slow one costs few requests.
const (
	firstPollInterval = 25 * time.Millisecond
	maxPollInterval   = 500 * time.Millisecond
	dialTimeout       = 2 * time.Second
)

// errLoopbackOnly is what a wait last saw when the only mongod running was
// one no published port can reach.
var errLoopbackOnly = errors.New("mongod is running but listening only on the container's own loopback, " +
	"which a published port cannot reach; it needs --bind_ip_all, which the image's entrypoint normally adds")

// readyProbe waits for a started container to be usable.
type readyProbe struct {
	docker dockerclient.Client
	logger dockerclient.Logger
	id     string
	name   string
	// host and port are the address a caller will connect to. hostSource
	// says what decided the host, for the error a failed wait returns.
	host       string
	port       int
	hostSource string
	timeout    time.Duration
	// requireAuth says the container was created with credentials, so the
	// mongod that serves must enforce them.
	requireAuth bool
}

// wait returns once the mongod that will serve is running and the published
// address accepts a connection.
//
// Both checks are needed. Docker accepts connections on a published port as
// soon as the container is created, long before mongod exists. And a process
// listing says nothing about whether the address reaches it.
//
// It does not prove mongod is answering commands yet: Docker accepts on its
// behalf. A caller should retry its first command, as a driver's ping does.
func (p readyProbe) wait(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, p.timeout)
	defer cancel()

	interval := firstPollInterval
	timer := time.NewTimer(interval)
	defer timer.Stop()

	var lastErr error
	for {
		ready, err := p.check(ctx)
		if ready || isFinal(err) {
			return err
		}
		if err != nil {
			lastErr = err
		}

		select {
		case <-ctx.Done():
			return p.notReady(ctx.Err(), lastErr)
		case <-timer.C:
			interval = min(interval*2, maxPollInterval)
			timer.Reset(interval)
		}
	}
}

// check makes one attempt. It reports ready, or an error: a final one ends
// the wait, and any other is only remembered for the timeout's message.
func (p readyProbe) check(ctx context.Context) (ready bool, err error) {
	serving, err := p.mongodIsServing(ctx)
	switch {
	case errors.Is(err, errLoopbackOnly), errors.Is(err, ErrAuthNotEnforced):
		return false, err
	case err != nil:
		// The listing fails both while a container is starting and after it
		// has died. Only the second is worth stopping for.
		if exited := p.exitedError(ctx, err); exited != nil {
			return false, exited
		}
		return false, err
	case !serving:
		return false, nil
	}
	if err := p.dial(ctx); err != nil {
		return false, err
	}
	p.logger.Debug("mongod is running and the published port answers",
		"container", p.name, "address", addr(p.host, p.port))
	return true, nil
}

// isFinal reports whether waiting longer cannot help.
func isFinal(err error) bool {
	return errors.Is(err, ErrAuthNotEnforced) || errors.Is(err, ErrContainerExited)
}

// notReady builds the error for a wait that ran out of time or was cancelled.
func (p readyProbe) notReady(cause, lastErr error) error {
	return &NotReadyError{
		Name: p.name, ID: p.id, Host: p.host, Port: p.port, HostSource: p.hostSource,
		Timeout: p.timeout, Cause: cause, LastErr: lastErr,
	}
}

// mongodIsServing reports whether the mongod a published port leads to is
// running. It returns errLoopbackOnly when the only mongod is the image's
// set-up one, and ErrAuthNotEnforced when the serving mongod ignores the
// credentials its container was created with.
func (p readyProbe) mongodIsServing(ctx context.Context) (bool, error) {
	top, err := p.docker.ContainerTop(ctx, p.id)
	if err != nil {
		return false, err
	}
	var setUpOnly bool
	for _, command := range commands(top) {
		flags, isMongod := mongodFlags(command)
		switch {
		case !isMongod:
			continue
		case !flags.servesPort():
			setUpOnly = true
		case p.requireAuth && !flags.enforcesAuth():
			return false, &AuthNotEnforcedError{Name: p.name, ID: p.id}
		default:
			return true, nil
		}
	}
	if setUpOnly {
		return false, errLoopbackOnly
	}
	return false, nil
}

// commandTitles are the names a process listing gives its command column.
var commandTitles = []string{"CMD", "COMMAND", "ARGS"}

// commands returns the command line of every process in a listing. The
// column is found by title, and is the last one if no title is recognised.
func commands(top dockerclient.Top) []string {
	column := max(len(top.Titles)-1, 0)
	for i, title := range top.Titles {
		if isCommandTitle(title) {
			column = i
			break
		}
	}
	var out []string
	for _, process := range top.Processes {
		if column < len(process) {
			out = append(out, process[column])
		}
	}
	return out
}

// isCommandTitle reports whether a column title names the command.
func isCommandTitle(title string) bool {
	for _, candidate := range commandTitles {
		if strings.EqualFold(strings.TrimSpace(title), candidate) {
			return true
		}
	}
	return false
}

// flags is a mongod command line reduced to its flags: each by name without
// its dashes, with the value it was given, if any.
type flags map[string]string

// mongodFlags reads a command line, and reports whether its program is
// mongod itself. The program has to be the first word: the official image's
// entrypoint is a script that takes "mongod" as an argument and runs as a
// user called mongodb, so the word appears before the server exists.
//
// mongod accepts a flag with one dash or two, and its value after "=" or as
// the next argument.
func mongodFlags(command string) (flags, bool) {
	args := strings.Fields(command)
	if len(args) == 0 || path.Base(args[0]) != "mongod" {
		return nil, false
	}
	args = args[1:]
	out := flags{}
	for i, arg := range args {
		if !strings.HasPrefix(arg, "-") {
			continue
		}
		name, value, inline := strings.Cut(strings.TrimLeft(arg, "-"), "=")
		if !inline {
			value = valueAfter(args, i)
		}
		out[name] = value
	}
	return out, true
}

// valueAfter returns the argument after position i, unless there is none or
// it is another flag.
func valueAfter(args []string, i int) string {
	if i+1 >= len(args) || strings.HasPrefix(args[i+1], "-") {
		return ""
	}
	return args[i+1]
}

// has reports whether any of the named flags was given.
func (f flags) has(names ...string) bool {
	for _, name := range names {
		if _, found := f[name]; found {
			return true
		}
	}
	return false
}

// servesPort reports whether this mongod listens where a published port can
// reach it, which is anywhere but the container's own loopback.
//
// mongod listens on localhost unless told otherwise, and images rely on that:
// to create a user or a replica set they first run a mongod only they can
// reach, then replace it with one given --bind_ip_all.
//
// An explicit address is believed before a configuration file, because the
// official entrypoint passes both to its set-up mongod. A file on its own
// may set the address and cannot be read from here, so it is trusted.
func (f flags) servesPort() bool {
	if f.has("shutdown") {
		return false // the entrypoint stopping its set-up mongod
	}
	if addresses, found := f["bind_ip"]; found {
		return reachesBeyondContainer(addresses)
	}
	return f.has("bind_ip_all", "config", "f")
}

// reachesBeyondContainer reports whether a comma-separated bind list has an
// address other than loopback or a unix socket.
func reachesBeyondContainer(addresses string) bool {
	for address := range strings.SplitSeq(addresses, ",") {
		local := address == "" || strings.HasPrefix(address, "/") || isLoopback(address)
		if !local {
			return true
		}
	}
	return false
}

// enforcesAuth reports whether this mongod makes clients authenticate. An
// image that has created a root user adds --auth; a key file implies it; a
// configuration file may ask for it where this cannot see.
func (f flags) enforcesAuth() bool {
	if f.has("transitionToAuth") {
		return false // accepts clients with or without credentials
	}
	return f.has("auth", "keyFile", "clusterAuthMode", "config", "f")
}

// exitedError returns an error if the container can never become ready
// because it has exited or been removed, and nil if it may still start.
func (p readyProbe) exitedError(ctx context.Context, cause error) error {
	info, err := p.docker.ContainerInspect(ctx, p.id)
	if dockerclient.IsNotFound(err) {
		return &ContainerExitedError{Name: p.name, ID: p.id, Status: "removed", Err: cause}
	}
	if err != nil {
		return nil // nothing is known, so keep waiting
	}
	if status := info.State.Status; status == "exited" || status == "dead" {
		return &ContainerExitedError{
			Name: p.name, ID: p.id, Status: status,
			ExitCode: info.State.ExitCode, HasExitCode: true, Err: cause,
		}
	}
	return nil
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
