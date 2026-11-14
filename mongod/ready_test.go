package mongod_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/tophergopher/mongotest/dockerclient"
	"github.com/tophergopher/mongotest/mongod"
)

// startWith starts a container whose process listing is scripted. The
// published port always answers, as it does from the moment a real container
// is created, so only the listing decides whether the start succeeds.
func startWith(t *testing.T, timeout time.Duration, commands func(poll int) []string, opts ...*mongod.Options) error {
	t.Helper()
	m, port := readyMock(t)
	var polls int
	m.ContainerTopFunc = func(context.Context, string) (dockerclient.Top, error) {
		polls++
		top := dockerclient.Top{Titles: []string{"PID", "CMD"}}
		for _, command := range commands(polls) {
			top.Processes = append(top.Processes, []string{"1", command})
		}
		return top, nil
	}
	opts = append(opts, mongod.WithDocker(m).WithPort(port).WithStartTimeout(timeout))
	c, err := mongod.Start(context.Background(), opts...)
	if err == nil {
		t.Cleanup(func() { _ = c.Stop(context.Background()) })
	}
	return err
}

// always is a process listing that never changes.
func always(command string) func(int) []string {
	return func(int) []string { return []string{command} }
}

// The command lines are the ones each image was seen running during a start.
func TestStartCountsOnlyAMongodThePortCanReach(t *testing.T) {
	cases := []struct {
		name    string
		command string
		ready   bool
	}{
		{"official image serving", "mongod --bind_ip_all", true},
		{"serving with auth and a replica set", "mongod --auth --replSet rs0 --keyFile /etc/keyfile --bind_ip_all", true},
		{"Atlas Local serving", "mongod --replSet 142b --keyFile /data/configdb/keyfile --bind_ip_all --auth", true},
		{"an explicit wildcard address", "mongod --bind_ip 0.0.0.0", true},
		{"an address list reaching beyond loopback", "mongod --bind_ip=localhost,172.17.0.2", true},
		{"flags with one dash", "mongod -bind_ip_all", true},
		{"a configuration file alone", "mongod --config /etc/mongod.conf", true},
		{"a configuration file, short flag", "mongod -f /etc/mongod.conf", true},
		{"given by path", "/usr/bin/mongod --bind_ip_all", true},

		{"official image creating its root user", "mongod --bind_ip 127.0.0.1 --port 27017 --fork", false},
		{"community image creating its root user", "/usr/bin/mongod --bind_ip 127.0.0.1 --port 27017 --fork", false},
		{"temporary mongod that was given a config file", "mongod --auth --config /tmp/temp-config.json --bind_ip 127.0.0.1 --fork", false},
		{"Atlas Local initiating its replica set", "mongod --replSet 142b --keyFile /data/configdb/keyfile --transitionToAuth", false},
		{"the entrypoint stopping its temporary mongod", "mongod --bind_ip_all --shutdown", false},
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
			err := startWith(t, timeout, always(tc.command))

			if tc.ready {
				assert.NoError(t, err, "a published port leads to this mongod")
				return
			}
			assert.ErrorIs(t, err, mongod.ErrNotReady, "nothing outside the container can reach this process")
		})
	}
}

func TestStartWaitsPastTheTemporaryMongod(t *testing.T) {
	var sawFinal bool
	err := startWith(t, 10*time.Second, func(poll int) []string {
		if poll <= 3 {
			return []string{"bash docker-entrypoint.sh mongod", "mongod --bind_ip 127.0.0.1 --fork"}
		}
		sawFinal = true
		return []string{"mongod --bind_ip_all"}
	})

	require.NoError(t, err, "the final mongod arrives inside the timeout")
	assert.True(t, sawFinal, "the temporary mongod is about to be shut down, so it does not count")
}

func TestAMongodStuckOnLoopbackIsNamedInTheError(t *testing.T) {
	err := startWith(t, 60*time.Millisecond, always("mongod --bind_ip 127.0.0.1"))

	require.ErrorIs(t, err, mongod.ErrNotReady, "it never became reachable")
	assert.ErrorIs(t, err, mongod.ErrLoopbackOnly, "so the caller can tell this from a mongod that never started")
}

func TestStartRequiresAuthWhenCredentialsWereSet(t *testing.T) {
	cases := []struct {
		name     string
		command  string
		enforced bool
	}{
		{"--auth", "mongod --auth --bind_ip_all", true},
		{"a key file implies it", "mongod --replSet rs0 --keyFile /etc/keyfile --bind_ip_all", true},
		{"a configuration file, which cannot be read to check", "mongod --config /etc/mongod.conf --bind_ip_all", false},
		{"the image ignored the credentials", "mongod --bind_ip_all", false},
		{"Atlas Local without a root user", "mongod --replSet 1f3c --keyFile /data/keyfile --bind_ip_all --transitionToAuth", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			started := time.Now()
			err := startWith(t, time.Minute, always(tc.command), mongod.WithAuth("admin", "secret"))

			if tc.enforced {
				assert.NoError(t, err, "this mongod makes clients authenticate")
				return
			}
			require.ErrorIs(t, err, mongod.ErrAuthNotEnforced, "an open mongod must not be returned behind a URI that claims credentials")
			assert.Less(t, time.Since(started), 10*time.Second, "this mongod will not change, so there is nothing to wait for")
		})
	}
}
