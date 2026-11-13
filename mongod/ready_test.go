package mongod

import (
	"context"
	"net"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/tophergopher/mongotest/dockerclient"
	"github.com/tophergopher/mongotest/dockermock"
)

// probeFor returns a readiness probe over a scripted process listing. The
// port it dials always answers, as a published port does from the moment a
// container is created, so only the listing decides the outcome.
func probeFor(t *testing.T, timeout time.Duration, commands func(poll int) []string) readyProbe {
	t.Helper()
	ln, err := net.Listen("tcp", net.JoinHostPort(loopback, "0"))
	require.NoError(t, err, "the probe dials a real socket")
	t.Cleanup(func() { _ = ln.Close() })
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			_ = conn.Close()
		}
	}()

	var polls int
	m := &dockermock.Mock{
		ContainerTopFunc: func(context.Context, string) (dockerclient.Top, error) {
			polls++
			top := dockerclient.Top{Titles: []string{"PID", "CMD"}}
			for _, command := range commands(polls) {
				top.Processes = append(top.Processes, []string{"1", command})
			}
			return top, nil
		},
	}
	return readyProbe{
		docker: m, logger: dockerclient.NopLogger(), id: "abc", name: "mongotest-abc",
		host: loopback, port: ln.Addr().(*net.TCPAddr).Port, timeout: timeout,
	}
}

// always is a process listing that never changes.
func always(command string) func(int) []string {
	return func(int) []string { return []string{command} }
}

// The command lines are the ones each image was seen running during a start.
func TestReadinessCountsOnlyAMongodThePortCanReach(t *testing.T) {
	cases := []struct {
		name    string
		command string
		ready   bool
	}{
		{"official image serving", "mongod --bind_ip_all", true},
		{"serving with auth and a replica set", "mongod --auth --replSet rs0 --keyFile /etc/mongotest-keyfile --bind_ip_all", true},
		{"Atlas Local serving", "mongod --replSet 142b --keyFile /data/configdb/keyfile --bind_ip_all --auth", true},
		{"an explicit wildcard address", "mongod --bind_ip 0.0.0.0", true},
		{"an address list reaching beyond loopback", "mongod --bind_ip=localhost,172.17.0.2", true},
		{"flags with one dash", "mongod -bind_ip_all", true},
		{"a configuration file alone", "mongod --config /etc/mongod.conf", true},
		{"a configuration file, short flag", "mongod -f /etc/mongod.conf", true},
		{"given by path", "/usr/bin/mongod --bind_ip_all", true},

		{"official image creating its root user", "mongod --bind_ip 127.0.0.1 --port 27017 --fork", false},
		{"community image creating its root user", "/usr/bin/mongod --bind_ip 127.0.0.1 --port 27017 --fork", false},
		{"set-up mongod that was given a config file", "mongod --auth --config /tmp/temp-config.json --bind_ip 127.0.0.1 --fork", false},
		{"Atlas Local initiating its replica set", "mongod --replSet 142b --keyFile /data/configdb/keyfile --transitionToAuth", false},
		{"the entrypoint stopping its set-up mongod", "mongod --bind_ip_all --shutdown", false},
		{"loopback addresses and a socket", "mongod --bind_ip localhost,::1,/tmp/mongod.sock", false},
		{"an address flag with no address", "mongod --bind_ip --quiet", false},
		{"no flags, so mongod's localhost default", "mongod", false},
		{"the entrypoint script, not mongod", "bash /usr/local/bin/docker-entrypoint.sh mongod", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			timeout := 5 * time.Second
			if !tc.ready {
				timeout = 60 * time.Millisecond
			}
			err := probeFor(t, timeout, always(tc.command)).wait(context.Background())

			if tc.ready {
				assert.NoError(t, err, "a published port leads to this mongod")
				return
			}
			assert.ErrorIs(t, err, ErrNotReady, "nothing outside the container can reach this process")
		})
	}
}

func TestReadinessWaitsPastTheSetUpMongod(t *testing.T) {
	var sawServing bool
	probe := probeFor(t, 10*time.Second, func(poll int) []string {
		if poll <= 3 {
			return []string{"bash docker-entrypoint.sh mongod", "mongod --bind_ip 127.0.0.1 --fork"}
		}
		sawServing = true
		return []string{"mongod --auth --bind_ip_all"}
	})

	require.NoError(t, probe.wait(context.Background()), "the serving mongod arrives inside the timeout")
	assert.True(t, sawServing, "the set-up mongod is about to be shut down, so it does not count")
}

func TestAMongodStuckOnLoopbackIsNamedInTheError(t *testing.T) {
	err := probeFor(t, 60*time.Millisecond, always("mongod --bind_ip 127.0.0.1")).wait(context.Background())

	require.ErrorIs(t, err, ErrNotReady, "it never became reachable")
	assert.ErrorIs(t, err, errLoopbackOnly, "so the caller can tell this from a mongod that never started")
}

func TestReadinessRequiresAuthWhenCredentialsWereSet(t *testing.T) {
	cases := []struct {
		name     string
		command  string
		enforced bool
	}{
		{"--auth", "mongod --auth --bind_ip_all", true},
		{"a key file implies it", "mongod --replSet rs0 --keyFile /etc/mongotest-keyfile --bind_ip_all", true},
		{"a configuration file may ask for it", "mongod --config /etc/mongod.conf --bind_ip_all", true},
		{"the image ignored the credentials", "mongod --bind_ip_all", false},
		{"Atlas Local without a root user", "mongod --replSet 1f3c --keyFile /data/keyfile --bind_ip_all --transitionToAuth", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			probe := probeFor(t, time.Minute, always(tc.command))
			probe.requireAuth = true

			started := time.Now()
			err := probe.wait(context.Background())

			if tc.enforced {
				assert.NoError(t, err, "this mongod makes clients authenticate")
				return
			}
			require.ErrorIs(t, err, ErrAuthNotEnforced, "an open mongod must not be returned behind a URI that claims credentials")
			assert.Less(t, time.Since(started), 10*time.Second, "this mongod will not change, so there is nothing to wait for")
		})
	}
}
