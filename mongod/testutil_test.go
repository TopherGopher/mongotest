package mongod_test

import (
	"context"
	"net"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/tophergopher/mongotest/dockerclient"
	"github.com/tophergopher/mongotest/dockermock"
	"github.com/tophergopher/mongotest/mongod"
)

// containerID is the id the mocked daemon gives every container.
const containerID = "3f1a9c4b7e2d5a8f0b6c3e9d1a4f7b2c5e8d0a3f6b9c2e5d8a1f4b7c0e3d6a9f"

// siblingGateway is an address that is not loopback, which makes Start
// publish on every interface.
const siblingGateway = "172.17.0.1"

// listenerPort opens a loopback socket that stands in for mongod's published
// port, and returns its port.
func listenerPort(t testing.TB) int {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err, "readiness dials a real socket")
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
	return ln.Addr().(*net.TCPAddr).Port
}

// closedPort returns a loopback port nothing is listening on.
func closedPort(t testing.TB) int {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err, "the test needs a free port")
	port := ln.Addr().(*net.TCPAddr).Port
	require.NoError(t, ln.Close(), "and nothing listening on it")
	return port
}

// processes is a process listing with one row per command line.
func processes(commands ...string) dockerclient.Top {
	top := dockerclient.Top{Titles: []string{"PID", "CMD"}}
	for _, command := range commands {
		top.Processes = append(top.Processes, []string{"1", command})
	}
	return top
}

// readyMock returns a mocked daemon whose container is running a serving
// mongod, and the port it reports the container published, which something
// really listens on.
func readyMock(t testing.TB) (*dockermock.Mock, int) {
	t.Helper()
	port := listenerPort(t)
	return &dockermock.Mock{
		ContainerCreateFunc: func(context.Context, string, dockerclient.ContainerConfig) (string, []string, error) {
			return containerID, nil, nil
		},
		ContainerInspectFunc: func(context.Context, string) (dockerclient.ContainerInspect, error) {
			return running(port), nil
		},
		ContainerTopFunc: func(context.Context, string) (dockerclient.Top, error) {
			return processes("mongod --bind_ip_all"), nil
		},
	}, port
}

// authMock is readyMock for a container created with credentials: its mongod
// enforces them, which Start insists on.
func authMock(t testing.TB) (*dockermock.Mock, int) {
	t.Helper()
	m, port := readyMock(t)
	m.ContainerTopFunc = func(context.Context, string) (dockerclient.Top, error) {
		return processes("mongod --auth --bind_ip_all"), nil
	}
	return m, port
}

// running is what the daemon reports for a running container whose mongod
// port is published on hostPort.
func running(hostPort int) dockerclient.ContainerInspect {
	return dockerclient.ContainerInspect{
		ID:    containerID,
		State: dockerclient.ContainerState{Status: "running", Running: true},
		NetworkSettings: dockerclient.NetworkSettings{Ports: map[string][]dockerclient.PortBinding{
			"27017/tcp": {{HostIP: "127.0.0.1", HostPort: strconv.Itoa(hostPort)}},
		}},
	}
}

// start starts a container against a mocked daemon and stops it when the
// test ends. The dial address is pinned to loopback so the result does not
// depend on where the tests run; an option passed in can still override it.
func start(t testing.TB, m *dockermock.Mock, opts ...mongod.Option) (*mongod.Container, error) {
	t.Helper()
	opts = append([]mongod.Option{mongod.WithDocker(m), mongod.WithHostIP("127.0.0.1")}, opts...)
	c, err := mongod.Start(context.Background(), opts...)
	if err == nil {
		t.Cleanup(func() { _ = c.Stop(context.Background()) })
	}
	return c, err
}

// mustStart is start for a test that is about something after the start.
func mustStart(t testing.TB, m *dockermock.Mock, opts ...mongod.Option) *mongod.Container {
	t.Helper()
	c, err := start(t, m, opts...)
	require.NoError(t, err, "the start must succeed")
	return c
}

// created returns the one create request a mocked daemon received.
func created(t testing.TB, m *dockermock.Mock) dockerclient.ContainerConfig {
	t.Helper()
	calls := m.CallsTo("ContainerCreate")
	require.Len(t, calls, 1, "exactly one container is created")
	cfg, ok := calls[0].Args[1].(dockerclient.ContainerConfig)
	require.True(t, ok, "the create call records its request")
	return cfg
}

// createdName returns the name of the one container a mocked daemon created.
func createdName(t testing.TB, m *dockermock.Mock) string {
	t.Helper()
	calls := m.CallsTo("ContainerCreate")
	require.Len(t, calls, 1, "exactly one container is created")
	name, _ := calls[0].Args[0].(string)
	return name
}

// requestFor returns the create request a set of options produces. The
// daemon never reports a mongod, so the start ends on a short timeout: the
// request is built before anything is waited for, and nothing has to listen
// on an address this machine may not have.
func requestFor(t testing.TB, opts ...mongod.Option) dockerclient.ContainerConfig {
	t.Helper()
	m := &dockermock.Mock{
		ContainerCreateFunc: func(context.Context, string, dockerclient.ContainerConfig) (string, []string, error) {
			return containerID, nil, nil
		},
		ContainerInspectFunc: func(context.Context, string) (dockerclient.ContainerInspect, error) {
			return running(1), nil
		},
	}
	opts = append(opts, mongod.WithStartTimeout(20*time.Millisecond))
	_, err := start(t, m, opts...)
	require.ErrorIs(t, err, mongod.ErrNotReady, "this daemon never reports a mongod")
	return created(t, m)
}

// flagValue returns the argument after a flag in a container command.
func flagValue(cmd []string, flag string) (string, bool) {
	for i, arg := range cmd {
		if arg == flag && i+1 < len(cmd) {
			return cmd[i+1], true
		}
	}
	return "", false
}

// parseURI parses a container's connection string.
func parseURI(t testing.TB, c *mongod.Container) *url.URL {
	t.Helper()
	parsed, err := url.Parse(c.URI())
	require.NoError(t, err, "the URI must be one a driver can parse")
	return parsed
}

// mustAtoi parses a number the daemon was given as a string.
func mustAtoi(t testing.TB, s string) int {
	t.Helper()
	n, err := strconv.Atoi(s)
	require.NoError(t, err, "%q must be a number", s)
	return n
}

// rootUser returns the root user a create request tells the image to
// create, or "" and "" if it asks for none. Images read one of two spellings
// of the variables, so both must be set and must agree.
func rootUser(t testing.TB, request dockerclient.ContainerConfig) (username, password string) {
	t.Helper()
	env := map[string]string{}
	for _, entry := range request.Env {
		key, value, _ := strings.Cut(entry, "=")
		env[key] = value
	}
	require.Equal(t, env["MONGO_INITDB_ROOT_USERNAME"], env["MONGODB_INITDB_ROOT_USERNAME"], "the official image and Atlas Local read different variables, so both carry the username")
	require.Equal(t, env["MONGO_INITDB_ROOT_PASSWORD"], env["MONGODB_INITDB_ROOT_PASSWORD"], "and both carry the password")
	return env["MONGO_INITDB_ROOT_USERNAME"], env["MONGO_INITDB_ROOT_PASSWORD"]
}
