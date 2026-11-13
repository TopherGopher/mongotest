// Package reaper removes what a process started when that process is told
// to stop, so an interrupted test run does not leave containers behind.
//
//	handle := reaper.Register("mongotest-1a2b3c4d", container.Stop)
//	defer reaper.Unregister(handle)
//
// A teardown is any function, so this package knows nothing about
// containers and anything with cleanup to do can register.
//
// # Reaping
//
// [Reap] runs every registered teardown, newest first, and needs no signal.
// Call it after m.Run in a TestMain:
//
//	code := m.Run()
//	_ = reaper.Reap(context.Background())
//	os.Exit(code)
//
// Every teardown runs even if one fails, and the failures come back together
// as a [ReapError].
//
// # Signals
//
// The first [Register] installs a handler for SIGINT and SIGTERM. Importing
// the package installs nothing, and a signal the process was started with
// ignored stays ignored.
//
// On a signal the handler reaps, for at most [DefaultReapTimeout], and then
// re-raises the signal so the process still dies of it. A second signal
// during the reap kills the process at once. SIGKILL cannot be handled.
package reaper
