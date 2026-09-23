package mongod_test

import (
	"context"
	"net"
	"strconv"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/tophergopher/mongotest/dockerclient"
	"github.com/tophergopher/mongotest/dockermock"
)

// The doubles in dockermock know nothing about mongod, and mongod's readiness
// probe is deliberately strict: it waits for a mongod process to appear in the
// container's process listing and then opens a real TCP connection to the
// published port. A double that only reports success is therefore not enough
// to let a start finish. The helpers here supply the two missing pieces, a
// process listing containing mongod and a socket something is really
// listening on, so that a test about anything else can get a started
// container without restating them.

// mongodProcesses is the process listing a container running mongod reports.
func mongodProcesses() dockerclient.Top {
	return dockerclient.Top{
		Titles:    []string{"PID", "USER", "TIME", "COMMAND"},
		Processes: [][]string{{"1", "mongodb", "0:00", "mongod --bind_ip_all"}},
	}
}

// listenerPort opens a socket on loopback that stands in for mongod and
// returns the port it is on. The listener is closed when the test ends.
func listenerPort(t *testing.T) int {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err, "the readiness probe dials a real socket, so a test that starts a container needs something listening")
	t.Cleanup(func() { _ = ln.Close() })
	// Accept and drop connections, so the probe's dial completes rather than
	// sitting in the accept backlog.
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

// readyMock returns a Mock that lets one start run to completion, and the
// host port it reports the container published. Every method is scripted, so
// a test can assert on the calls without the double deciding anything.
func readyMock(t *testing.T) (*dockermock.Mock, int) {
	t.Helper()
	port := listenerPort(t)
	const id = "3f1a9c4b7e2d5a8f0b6c3e9d1a4f7b2c5e8d0a3f6b9c2e5d8a1f4b7c0e3d6a9f"
	m := &dockermock.Mock{
		ContainerCreateFunc: func(ctx context.Context, name string, cfg dockerclient.ContainerConfig) (string, []string, error) {
			return id, nil, nil
		},
		ContainerInspectFunc: func(ctx context.Context, cid string) (dockerclient.ContainerInspect, error) {
			return inspectPublishing(cid, port), nil
		},
		ContainerTopFunc: func(ctx context.Context, cid string) (dockerclient.Top, error) {
			return mongodProcesses(), nil
		},
	}
	return m, port
}

// inspectPublishing is what the daemon reports for a running container whose
// 27017/tcp is published on hostPort.
func inspectPublishing(id string, hostPort int) dockerclient.ContainerInspect {
	return dockerclient.ContainerInspect{
		ID:    id,
		Name:  "/mongotest-0a1b2c3d",
		State: dockerclient.ContainerState{Status: "running", Running: true},
		NetworkSettings: dockerclient.NetworkSettings{Ports: map[string][]dockerclient.PortBinding{
			"27017/tcp": {{HostIP: "127.0.0.1", HostPort: strconv.Itoa(hostPort)}},
		}},
	}
}

// readyFake returns a Fake that lets starts run to completion, and a host port
// to pin with WithPort so the readiness probe finds something listening. The
// Fake enforces the daemon's real rules, so it is the right double whenever a
// test is about the lifecycle rather than about the calls themselves.
func readyFake(t *testing.T) (*dockermock.Fake, int) {
	t.Helper()
	f := dockermock.NewFake(dockermock.WithImages("mongo:8"))
	f.Processes = fakeMongodProcesses
	return f, listenerPort(t)
}

// fakeMongodProcesses is the process listing for the Fake, whose Top reports
// the titles PID and CMD. The rows have to line up with those two columns:
// readiness reads the command column by name, so a row shaped for some other
// listing puts a user name or a timestamp where the command should be.
func fakeMongodProcesses(dockermock.ContainerState) [][]string {
	return [][]string{{"1", "mongod --bind_ip_all"}}
}

// createdConfig returns the ContainerConfig recorded by the single
// ContainerCreate call the test expected, failing the test when there was not
// exactly one.
func createdConfig(t *testing.T, m *dockermock.Mock) dockerclient.ContainerConfig {
	t.Helper()
	calls := m.CallsTo("ContainerCreate")
	require.Len(t, calls, 1, "the create request is what this test asserts on, so exactly one create must have happened")
	require.Len(t, calls[0].Args, 2, "ContainerCreate records the name and the config after the context")
	cfg, ok := calls[0].Args[1].(dockerclient.ContainerConfig)
	require.True(t, ok, "the second recorded argument of ContainerCreate is the ContainerConfig")
	return cfg
}

// createdName returns the container name passed to the single ContainerCreate
// call.
func createdName(t *testing.T, m *dockermock.Mock) string {
	t.Helper()
	calls := m.CallsTo("ContainerCreate")
	require.Len(t, calls, 1, "the create request is what this test asserts on, so exactly one create must have happened")
	name, ok := calls[0].Args[0].(string)
	require.True(t, ok, "the first recorded argument of ContainerCreate is the container name")
	return name
}

// recordingLogger captures what mongod logs, so a test can prove the logger
// an option supplied is the one actually used.
type recordingLogger struct {
	mu       sync.Mutex
	messages []string
}

func (l *recordingLogger) record(msg string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.messages = append(l.messages, msg)
}

func (l *recordingLogger) Messages() []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]string(nil), l.messages...)
}

func (l *recordingLogger) Debug(msg string, _ ...any) { l.record(msg) }
func (l *recordingLogger) Info(msg string, _ ...any)  { l.record(msg) }
func (l *recordingLogger) Warn(msg string, _ ...any)  { l.record(msg) }
func (l *recordingLogger) Error(msg string, _ ...any) { l.record(msg) }

var _ dockerclient.Logger = (*recordingLogger)(nil)

// strconvItoa keeps the environment-variable tests readable without each of
// them importing strconv for one call.
func strconvItoa(i int) string { return strconv.Itoa(i) }
