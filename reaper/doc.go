// Package reaper tears down what a process started, when that process is told
// to stop.
//
// A test binary that is interrupted between starting a container and stopping it
// leaves the container running: mongod holding a port, a volume on disk, and
// nothing to say where it came from. This package is the one thing in the
// process that can do something about that, because a signal arrives once for
// the process rather than once per caller.
//
// # Registering
//
//	handle := reaper.Register("mongotest-1a2b3c4d", container.Stop)
//	defer reaper.Unregister(handle)
//
// Register takes a name, for the message a failure produces, and a function
// that tears one thing down. It returns a handle that gives the registration
// back. Whoever stops the thing normally unregisters it, so that a reap does not
// remove it a second time.
//
// Teardowns are functions rather than any particular type, so the dependency
// points one way: mongod imports this package, and this package knows nothing
// about containers. Anything else with cleanup to do can register too.
//
// # Reaping, with or without a signal
//
// [Reap] runs every registered teardown and is an ordinary function call:
//
//	defer reaper.Reap(ctx)                  // in a TestMain
//	err := mongod.ReapRunningContainers(ctx) // the same thing, named for the caller
//
// That matters for two reasons. A process can end in ways no handler sees, so an
// explicit reap is the only way to be sure. And it makes the signal path thin:
// the handler calls the same exported function everything else does, which is
// what lets it be tested without sending a signal.
//
// Teardown runs in reverse registration order, since later things are the more
// likely to depend on earlier ones. Every teardown runs even when one fails,
// because the containers left behind are the thing being prevented; the failures
// are joined into a [ReapError] that names each one.
//
// # The signal handler
//
// The first [Register] installs a handler for [DefaultSignals], which is SIGINT
// and SIGTERM and nothing else. Importing this package installs nothing: a
// handler installed from a package initialiser changes how every binary that
// imports it responds to Ctrl-C, whether or not it ever starts a container.
//
// When a signal arrives the handler removes itself first, so that a second
// Ctrl-C kills the process at once -- someone pressing it twice has said they
// are done waiting. Then it reaps under [DefaultReapTimeout]. Then it re-raises
// the signal, so the process dies of what it was sent.
//
// That last step is not a detail. A handler that reaps and returns leaves the
// process running, so Ctrl-C stops terminating it: the predecessor of this
// package did exactly that. The behaviour is covered by an integration test that
// signals a real subprocess and asserts both that the container is gone and that
// the process died of the signal, because either half alone would have passed
// against that predecessor.
//
// SIGKILL is not handled, because it cannot be: listening for it only suggests a
// guarantee that does not exist. SIGQUIT is left alone so that the Go runtime can
// still dump goroutine stacks on it.
package reaper
