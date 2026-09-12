package mongod

import (
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
