// Package reaper removes what a process started when that process is told
// to stop, so an interrupted test run does not leave containers behind. It
// is internal: mongod and the legacy root package register with it.
//
//	handle := reaper.Register("mongotest-1a2b3c4d", container.Stop)
//	defer reaper.Unregister(handle)
//
// [Reap] runs every registered teardown, newest first, and needs no signal.
// Every teardown runs even if one fails, and the failures come back together
// as a [ReapError].
//
// The first [Register] installs a handler for SIGINT and SIGTERM. Importing
// the package installs nothing, and a signal the process was started with
// ignored stays ignored. On a signal the handler reaps, for at most
// [DefaultReapTimeout], and then re-raises the signal so the process still
// dies of it. A second signal during the reap kills the process at once.
package reaper
