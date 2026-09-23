package reaper

import (
	"context"
	"errors"
	"testing"
)

// The registry is on the path of every container start and stop, so what is
// measured here is the bookkeeping rather than any teardown.

func noTeardown(context.Context) error { return nil }

func BenchmarkRegister(b *testing.B) {
	b.Cleanup(stopForTest)
	b.ReportAllocs()
	for b.Loop() {
		Unregister(Register("mongotest-1a2b3c4d", noTeardown))
	}
}

func BenchmarkUnregister(b *testing.B) {
	b.Cleanup(stopForTest)
	b.ReportAllocs()
	for b.Loop() {
		b.StopTimer()
		handle := Register("mongotest-1a2b3c4d", noTeardown)
		b.StartTimer()

		Unregister(handle)
	}
}

// BenchmarkUnregisterFromManyRegistered is the shape that matters: a suite with
// many containers up at once unregisters from the middle of the slice, which is
// a linear scan. If this ever stops being cheap enough, the registry wants a map
// beside the slice rather than a different lock.
func BenchmarkUnregisterFromManyRegistered(b *testing.B) {
	b.Cleanup(stopForTest)
	const registered = 64
	handles := make([]Handle, 0, registered)
	for range registered {
		handles = append(handles, Register("mongotest-1a2b3c4d", noTeardown))
	}
	b.ReportAllocs()
	for b.Loop() {
		b.StopTimer()
		middle := Register("mongotest-in-the-middle", noTeardown)
		b.StartTimer()

		Unregister(middle)
	}
	for _, h := range handles {
		Unregister(h)
	}
}

func BenchmarkReap(b *testing.B) {
	b.Cleanup(stopForTest)
	ctx := context.Background()
	b.ReportAllocs()
	for b.Loop() {
		b.StopTimer()
		for range 8 {
			Register("mongotest-1a2b3c4d", noTeardown)
		}
		b.StartTimer()

		if err := Reap(ctx); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkReapEmpty(b *testing.B) {
	// The deferred-unconditionally case: a run that started nothing.
	b.Cleanup(stopForTest)
	ctx := context.Background()
	b.ReportAllocs()
	for b.Loop() {
		if err := Reap(ctx); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkNames(b *testing.B) {
	b.Cleanup(stopForTest)
	for range 16 {
		Register("mongotest-1a2b3c4d", noTeardown)
	}
	b.ReportAllocs()
	for b.Loop() {
		_ = Names()
	}
}

func BenchmarkInstalled(b *testing.B) {
	b.Cleanup(stopForTest)
	Install()
	b.ReportAllocs()
	for b.Loop() {
		_ = Installed()
	}
}

func BenchmarkDefaultSignals(b *testing.B) {
	b.ReportAllocs()
	for b.Loop() {
		_ = DefaultSignals()
	}
}

func BenchmarkReapErrorMessage(b *testing.B) {
	single := &ReapError{Errs: []error{errors.New("reaper: cannot tear down mongotest-1a2b3c4d: device or resource busy")}}
	several := &ReapError{Errs: []error{
		errors.New("reaper: cannot tear down mongotest-1a2b3c4d: device or resource busy"),
		errors.New("reaper: cannot tear down mongotest-5e6f7a8b: in use"),
	}}
	for name, err := range map[string]error{"one": single, "several": several} {
		b.Run(name, func(b *testing.B) {
			b.ReportAllocs()
			for b.Loop() {
				_ = err.Error()
			}
		})
	}
}
