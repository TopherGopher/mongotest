package mongod

import (
	"context"
	"errors"
	"path"
	"slices"
	"strings"
	"time"

	"github.com/tophergopher/mongotest/dockerclient"
)

// Readiness is polled. The interval doubles from the first to the maximum,
// so a quick start is noticed at once and a slow one costs few requests.
const (
	firstPollInterval = 25 * time.Millisecond
	maxPollInterval   = 500 * time.Millisecond
)

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
	// hasHealthCheck says the image declares a health check to wait for.
	hasHealthCheck bool
}

// wait polls until the container is ready, the timeout passes, or the
// container can no longer become ready.
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
		// A check cut short by the timeout says nothing new.
		if err != nil && ctx.Err() == nil {
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

// check makes one attempt. Each step answers what the one before cannot:
// the process listing says which mongod is running, the hello says the
// published address reaches it and it is answering, and the image's health
// check says an image that replaces its mongod has finished doing so.
func (p readyProbe) check(ctx context.Context) (ready bool, err error) {
	state, err := p.mongodState(ctx)
	if err != nil {
		// The listing fails both while a container is starting and after it
		// has died. Only the second is worth stopping for.
		if exited := p.exitedError(ctx, err); exited != nil {
			return false, exited
		}
		return false, err
	}
	switch state {
	case notRunning:
		return false, nil
	case settingUp:
		return false, ErrLoopbackOnly
	case servingWithoutAuth:
		return false, &AuthNotEnforcedError{Name: p.name, ID: p.id}
	}
	if err := p.hello(ctx); err != nil {
		return false, err
	}
	if err := p.healthy(ctx); err != nil {
		return false, err
	}
	p.logger.Debug("mongod answers on the published port", "container", p.name, "address", addr(p.host, p.port))
	return true, nil
}

// errNotHealthy is why a wait on a health check failed, inside the
// NotReadyError a caller sees.
var errNotHealthy = errors.New("the image's own health check has not passed")

// healthy waits on the health check an image declares, and passes at once
// for an image that declares none.
func (p readyProbe) healthy(ctx context.Context) error {
	if !p.hasHealthCheck {
		return nil
	}
	info, err := p.docker.ContainerInspect(ctx, p.id)
	if err != nil {
		return err
	}
	if info.State.Health.Status != healthStatusHealthy {
		return errNotHealthy
	}
	return nil
}

// healthStatusHealthy is what the daemon reports once a health check passes.
const healthStatusHealthy = "healthy"

// declaresHealthCheck reports whether a container's image has a health
// check, from the status the daemon gives a container that has none.
func declaresHealthCheck(status string) bool {
	return status != "" && status != "none"
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

// mongodState is how far the container has got towards a usable mongod.
type mongodState int

const (
	// notRunning: no mongod process yet.
	notRunning mongodState = iota
	// settingUp: only the image's temporary mongod is running. An image that
	// creates a user or a replica set first runs a mongod only it can reach,
	// on the container's loopback, and then replaces it.
	settingUp
	// serving: the final mongod is running.
	serving
	// servingWithoutAuth: the final mongod is running but ignores the
	// credentials its container was created with.
	servingWithoutAuth
)

// mongodState reads the container's process listing.
func (p readyProbe) mongodState(ctx context.Context) (mongodState, error) {
	top, err := p.docker.ContainerTop(ctx, p.id)
	if err != nil {
		return notRunning, err
	}
	state := notRunning
	for _, command := range commands(top) {
		flags, isMongod := mongodFlags(command)
		switch {
		case !isMongod:
			continue
		case !flags.listensBeyondLoopback():
			state = settingUp
		case p.requireAuth && !flags.enforcesAuth():
			return servingWithoutAuth, nil
		default:
			return serving, nil
		}
	}
	return state, nil
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
	title = strings.TrimSpace(title)
	return slices.ContainsFunc(commandTitles, func(candidate string) bool {
		return strings.EqualFold(title, candidate)
	})
}

// flags is a mongod command line reduced to its flags: each by name without
// its dashes, with the value it was given, if any.
type flags map[string]string

// mongodFlags reads a command line, and reports whether its program is
// mongod itself. The program has to be the first word: the official image's
// entrypoint is a script that takes "mongod" as an argument, so the word
// appears before the server exists.
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

// listensBeyondLoopback reports whether this mongod listens where a
// published port can reach it. mongod listens on localhost unless told
// otherwise, which is what the temporary mongod of settingUp relies on.
//
// An explicit address is believed before a configuration file, because the
// official entrypoint passes both to its temporary mongod. A file on its own
// may set the address and cannot be read from here, so it is trusted.
func (f flags) listensBeyondLoopback() bool {
	if f.has("shutdown") {
		return false // the entrypoint stopping its temporary mongod
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
		if !isContainerLocal(address) {
			return true
		}
	}
	return false
}

// isContainerLocal reports whether only the container itself can connect to
// a bind address: nothing, a unix socket path, or loopback.
func isContainerLocal(address string) bool {
	if address == "" || strings.HasPrefix(address, "/") {
		return true
	}
	return isLoopback(address)
}

// enforcesAuth reports whether this mongod makes clients authenticate. An
// image that has created a root user adds --auth, and a key file implies it.
// A configuration file might ask for it too, but cannot be read from here,
// so it does not count: an open database must not pass on a guess.
func (f flags) enforcesAuth() bool {
	if f.has("transitionToAuth") {
		return false // accepts clients with or without credentials
	}
	return f.has("auth", "keyFile", "clusterAuthMode")
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
