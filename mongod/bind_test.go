package mongod

import (
	"net"
	"testing"

	"github.com/stretchr/testify/assert"
)

// Docker takes HostIP literally: docker-proxy listens on that address alone
// and the DNAT rule matches that address alone. The bind therefore has to
// follow the address the caller was told to dial, or the resolution produces
// a correct address for a port that is not there.
func TestBindIPFollowsTheResolvedAddress(t *testing.T) {
	cases := []struct {
		name     string
		resolved string
		want     string
		why      string
	}{
		{
			name: "loopback stays on loopback", resolved: "127.0.0.1", want: "127.0.0.1",
			why: "the laptop case dials loopback, and keeping the bind there keeps an unauthenticated database off the machine's other interfaces",
		},
		{
			name: "another loopback address is still loopback", resolved: "127.0.1.1", want: "127.0.0.1",
			why: "the whole 127/8 range is this machine, so there is nothing to widen the bind for",
		},
		{
			name: "the ipv6 loopback is loopback", resolved: "::1", want: "127.0.0.1",
			why: "mongod is published over ipv4 here, and ::1 still means a caller on this machine",
		},
		{
			name: "the name localhost is loopback", resolved: "localhost", want: "127.0.0.1",
			why: "MONGOTEST_HOST_IP=localhost says the same thing as 127.0.0.1 and should not widen the bind",
		},
		{
			name: "a bridge gateway needs every interface", resolved: "172.17.0.1", want: "0.0.0.0",
			why: "a sibling container's packets arrive on docker0, and a port bound to loopback refuses them; this is the case that was broken",
		},
		{
			name: "a remote daemon host needs every interface", resolved: "build-host", want: "0.0.0.0",
			why: "the port is published on the build host's interfaces, and the caller arrives over the network",
		},
		{
			name: "an address on this machine needs every interface", resolved: "10.1.2.3", want: "0.0.0.0",
			why: "the daemon may not be able to bind the address the caller dials, so the bind covers all of them and lets routing decide",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, bindIP(tc.resolved), tc.why)
		})
	}
}

// The bind and the dial are two halves of one address. bindIP narrows every
// loopback address to 127.0.0.1, because that is what docker-proxy publishes
// on, so a caller left dialling ::1 or 127.0.1.1 gets a connection refused by
// a port that is listening a few bytes away.
func TestDialHostMatchesWhatTheBindActuallyListensOn(t *testing.T) {
	cases := []struct {
		name     string
		resolved string
		want     string
		why      string
	}{
		{
			name: "the ipv6 loopback is dialled over ipv4", resolved: "::1", want: "127.0.0.1",
			why: "WithHostIP(\"::1\"), MONGOTEST_HOST_IP=::1 and DOCKER_HOST=tcp://[::1]:2375 all land here; the bind is ipv4 loopback, so dialling [::1] is refused",
		},
		{
			name: "another loopback address is dialled at the one bound", resolved: "127.0.1.1", want: "127.0.0.1",
			why: "the bind narrows the whole 127/8 range to 127.0.0.1, and docker-proxy listens on that address alone",
		},
		{
			name: "loopback is already what is bound", resolved: "127.0.0.1", want: "127.0.0.1",
			why: "the ordinary case must not be rewritten into something else",
		},
		{
			name: "a name is left to the resolver", resolved: "localhost", want: "localhost",
			why: "a name has more than one answer and the dialler tries each, so localhost reaches an ipv4 bind on its own; rewriting it would throw away what the caller asked to see in the URI",
		},
		{
			name: "a bridge gateway is dialled as given", resolved: "172.17.0.1", want: "172.17.0.1",
			why: "the bind for it is every interface, so there is nothing to narrow to",
		},
		{
			name: "a remote daemon host is dialled as given", resolved: "build-host", want: "build-host",
			why: "only the caller can resolve it, which is why the bind covers every interface rather than that address",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := dialHost(tc.resolved)

			assert.Equal(t, tc.want, got, tc.why)
			if ip := net.ParseIP(got); ip != nil {
				assert.Equal(t, bindIP(got), bindIP(tc.resolved),
					"narrowing the dial must not change which interface the port is published on, or the two would drift apart again")
				assert.True(t, !ip.IsLoopback() || bindIP(got) == got,
					"a loopback address has to be the exact one docker-proxy binds, because the DNAT rule matches that address alone")
			}
		})
	}
}
