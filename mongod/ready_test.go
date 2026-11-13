package mongod

import (
	"context"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/tophergopher/mongotest/dockerclient"
	"github.com/tophergopher/mongotest/dockermock"
)

// The command lines here are the ones each image family was measured running,
// read from the daemon's process listing during a start.
func TestServesPortTellsTheSetUpMongodFromTheRealOne(t *testing.T) {
	cases := []struct {
		name    string
		command string
		want    bool
		why     string
	}{
		{
			name:    "official image, creating the root user",
			command: "mongod --bind_ip 127.0.0.1 --port 27017 --tlsMode disabled --logpath /proc/1/fd/1 --logappend --pidfilepath /tmp/docker-entrypoint-temp-mongod.pid --fork",
			why:     "the entrypoint runs this to create the user and shuts it down again; nothing outside the container can connect to it",
		},
		{
			name:    "community image, creating the root user",
			command: "/usr/bin/mongod --bind_ip 127.0.0.1 --port 27017 --logappend --tlsMode disabled --logpath /proc/1/fd/1 --fork",
			why:     "MongoDB's own build does the same thing, by path",
		},
		{
			name:    "Atlas Local, initiating its replica set",
			command: "mongod --replSet 142b15014d23 --dbpath /data/db --keyFile /data/configdb/keyfile --maxConns 32200 --transitionToAuth",
			why:     "with no bind flag at all mongod listens on localhost, which is what the runner relies on while it sets the replica set up",
		},
		{
			name:    "official image, serving",
			command: "mongod --bind_ip_all",
			want:    true,
			why:     "this is the server the published port leads to",
		},
		{
			name:    "official image, serving with authentication and a replica set",
			command: "mongod --auth --replSet rs0 --keyFile /etc/mongotest-keyfile --bind_ip_all",
			want:    true,
			why:     "where the flag is among the others makes no difference",
		},
		{
			name:    "Atlas Local, serving",
			command: "mongod --replSet 142b15014d23 --dbpath /data/db --keyFile /data/configdb/keyfile --maxConns 32200 --bind_ip_all --setParameter mongotHost=localhost:27027 --auth",
			want:    true,
			why:     "the second mongod Atlas Local runs is the one that stays",
		},
		{
			name:    "an explicit wildcard address",
			command: "mongod --bind_ip 0.0.0.0",
			want:    true,
			why:     "a caller's own --bind_ip is honoured when it reaches beyond the container",
		},
		{
			name:    "an address list that includes the container's own",
			command: "mongod --bind_ip=localhost,172.17.0.2",
			want:    true,
			why:     "one reachable address in the list is enough, and the inline form of the flag is read too",
		},
		{
			name:    "an address list of loopbacks and a socket",
			command: "mongod --bind_ip localhost,::1,/tmp/mongod.sock",
			why:     "none of these can be reached through a published port",
		},
		{
			name:    "a configuration file",
			command: "mongod --config /etc/mongod.conf",
			want:    true,
			why:     "the file decides the address and cannot be read from here, so the mongod is taken at its word rather than waited on forever",
		},
		{
			name:    "a configuration file, short form",
			command: "mongod -f /etc/mongod.conf",
			want:    true,
			why:     "-f is the same flag",
		},
		{
			name:    "no flags at all",
			command: "mongod",
			why:     "mongod listens on localhost unless told otherwise",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			args := strings.Fields(tc.command)[1:]

			assert.Equal(t, tc.want, servesPort(args), tc.why)
		})
	}
}

// probeFor returns a readiness probe over a scripted process listing. The
// port it dials answers throughout, as docker-proxy does from the moment a
// container is created: only the process listing can tell the phases apart.
func probeFor(t *testing.T, timeout time.Duration, top func(poll int) [][]string) readyProbe {
	t.Helper()
	ln, err := net.Listen("tcp", net.JoinHostPort(loopback, "0"))
	require.NoError(t, err, "the probe dials a real socket, so something has to be listening")
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
			return dockerclient.Top{Titles: []string{"PID", "CMD"}, Processes: top(polls)}, nil
		},
	}
	return readyProbe{
		docker: m, logger: dockerclient.NopLogger(), id: "abc", name: "mongotest-abc",
		host: loopback, port: ln.Addr().(*net.TCPAddr).Port, timeout: timeout,
	}
}

func TestReadinessWaitsPastTheSetUpMongod(t *testing.T) {
	const setUpPolls = 3
	var sawServing bool
	probe := probeFor(t, 10*time.Second, func(poll int) [][]string {
		if poll <= setUpPolls {
			return [][]string{{"1", "bash /usr/local/bin/docker-entrypoint.sh mongod"}, {"30", "mongod --bind_ip 127.0.0.1 --port 27017 --fork"}}
		}
		sawServing = true
		return [][]string{{"1", "mongod --auth --bind_ip_all"}}
	})
	require.NoError(t, probe.wait(context.Background()), "the real mongod arrives well inside the budget")

	assert.True(t, sawServing, "a mongod that listens only inside the container is the image setting itself up; returning while it runs hands the caller a server that is about to be shut down")
}

func TestAMongodThatNeverLeavesLoopbackIsExplained(t *testing.T) {
	probe := probeFor(t, 150*time.Millisecond, func(int) [][]string {
		return [][]string{{"1", "mongod --bind_ip 127.0.0.1"}}
	})
	err := probe.wait(context.Background())

	require.ErrorIs(t, err, ErrNotReady, "a mongod nothing can reach is not ready, however long it runs")
	assert.ErrorIs(t, err, errLoopbackOnly, "the last failure is carried, so the error can be told apart from a mongod that never started")
	assert.Contains(t, err.Error(), "--bind_ip_all", "the message says what the mongod is missing, because a timeout alone sends the reader to the network")
}
