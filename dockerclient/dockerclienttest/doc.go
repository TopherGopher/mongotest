// Package dockerclienttest holds the suites every dockerclient
// implementation is held to: one conformance suite that asserts they behave
// the same way, and one benchmark suite that measures them on the same
// scale.
//
// An implementation runs both from its own test file:
//
//	func TestConformance(t *testing.T) {
//		dockerclienttest.Conformance(t, func(t *testing.T) dockerclient.Client {
//			d := dockermock.NewDaemon()
//			t.Cleanup(d.Close)
//			d.ServeFake(dockermock.NewFake())
//			c, err := dockerapi.New(dockerapi.WithHost(d.Host()))
//			require.NoError(t, err)
//			return c
//		})
//	}
//
//	func BenchmarkClient(b *testing.B) {
//		dockerclienttest.Benchmarks(b, func(b *testing.B) dockerclient.Client { ... })
//	}
//
// The factory must return a client whose daemon starts empty and keeps
// state: the suite pulls an image, creates a container from it, starts it,
// inspects it, execs in it and removes it, then checks it is gone. Point a
// real client at a dockermock.Daemon wired with ServeFake, or hand back a
// dockermock.Fake directly.
//
// Because the same assertions run against every implementation, a difference
// in behaviour shows up as a failure rather than as a surprise the first
// time somebody swaps their client.
package dockerclienttest
