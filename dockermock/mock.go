package dockermock

import (
	"context"
	"io"
	"sync"

	"github.com/tophergopher/mongotest/dockerclient"
)

// Mock is a dockerclient.Client whose behaviour is supplied per method and
// which records every call. A nil function field means the method succeeds
// and returns the zero result, so a test scripts only what it cares about.
//
// It is safe for concurrent use.
type Mock struct {
	HostFunc                   func() string
	ImagePullFunc              func(ctx context.Context, ref string) error
	ImageInspectFunc           func(ctx context.Context, ref string) (dockerclient.ImageInspect, error)
	ContainerCreateFunc        func(ctx context.Context, name string, cfg dockerclient.ContainerConfig) (string, []string, error)
	ContainerStartFunc         func(ctx context.Context, id string) error
	ContainerRemoveFunc        func(ctx context.Context, id string, opts dockerclient.RemoveOptions) error
	ContainerInspectFunc       func(ctx context.Context, id string) (dockerclient.ContainerInspect, error)
	ContainerTopFunc           func(ctx context.Context, id string) (dockerclient.Top, error)
	CopyToContainerFunc        func(ctx context.Context, id, destDir string, files []dockerclient.File) error
	CopyArchiveToContainerFunc func(ctx context.Context, id, destDir string, archive io.Reader) error
	ExecCreateFunc             func(ctx context.Context, containerID string, cfg dockerclient.ExecConfig) (string, error)
	ExecStartToFunc            func(ctx context.Context, execID string, stdout, stderr io.Writer) error
	ExecInspectFunc            func(ctx context.Context, execID string) (dockerclient.ExecInspect, error)
	ExecFunc                   func(ctx context.Context, containerID string, cmd ...string) (dockerclient.ExecResult, error)

	mu    sync.Mutex
	calls []Call
}

// Call is one recorded method call. Args holds the arguments after the
// context, in order.
type Call struct {
	Method string
	Args   []any
}

// Calls returns every call made so far, in order.
func (m *Mock) Calls() []Call {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]Call, len(m.calls))
	copy(out, m.calls)
	return out
}

// CallsTo returns the calls made to one method, in order. It is the usual
// way to assert what happened: len(m.CallsTo("ImagePull")) == 1.
func (m *Mock) CallsTo(method string) []Call {
	var out []Call
	for _, c := range m.Calls() {
		if c.Method == method {
			out = append(out, c)
		}
	}
	return out
}

// Methods returns the names of the calls made so far, in order, which is
// convenient for asserting a whole sequence in one comparison.
func (m *Mock) Methods() []string {
	calls := m.Calls()
	out := make([]string, len(calls))
	for i, c := range calls {
		out[i] = c.Method
	}
	return out
}

// Reset forgets the recorded calls. The function fields are left alone.
func (m *Mock) Reset() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.calls = nil
}

func (m *Mock) record(method string, args ...any) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.calls = append(m.calls, Call{Method: method, Args: args})
}

func (m *Mock) Host() string {
	m.record("Host")
	if m.HostFunc != nil {
		return m.HostFunc()
	}
	return "mock://dockermock"
}

func (m *Mock) ImagePull(ctx context.Context, ref string) error {
	m.record("ImagePull", ref)
	if m.ImagePullFunc != nil {
		return m.ImagePullFunc(ctx, ref)
	}
	return nil
}

func (m *Mock) ImageInspect(ctx context.Context, ref string) (dockerclient.ImageInspect, error) {
	m.record("ImageInspect", ref)
	if m.ImageInspectFunc != nil {
		return m.ImageInspectFunc(ctx, ref)
	}
	return dockerclient.ImageInspect{}, nil
}

func (m *Mock) ContainerCreate(ctx context.Context, name string, cfg dockerclient.ContainerConfig) (string, []string, error) {
	m.record("ContainerCreate", name, cfg)
	if m.ContainerCreateFunc != nil {
		return m.ContainerCreateFunc(ctx, name, cfg)
	}
	return "", nil, nil
}

func (m *Mock) ContainerStart(ctx context.Context, id string) error {
	m.record("ContainerStart", id)
	if m.ContainerStartFunc != nil {
		return m.ContainerStartFunc(ctx, id)
	}
	return nil
}

func (m *Mock) ContainerRemove(ctx context.Context, id string, opts dockerclient.RemoveOptions) error {
	m.record("ContainerRemove", id, opts)
	if m.ContainerRemoveFunc != nil {
		return m.ContainerRemoveFunc(ctx, id, opts)
	}
	return nil
}

func (m *Mock) ContainerInspect(ctx context.Context, id string) (dockerclient.ContainerInspect, error) {
	m.record("ContainerInspect", id)
	if m.ContainerInspectFunc != nil {
		return m.ContainerInspectFunc(ctx, id)
	}
	return dockerclient.ContainerInspect{}, nil
}

func (m *Mock) ContainerTop(ctx context.Context, id string) (dockerclient.Top, error) {
	m.record("ContainerTop", id)
	if m.ContainerTopFunc != nil {
		return m.ContainerTopFunc(ctx, id)
	}
	return dockerclient.Top{}, nil
}

func (m *Mock) CopyToContainer(ctx context.Context, id, destDir string, files []dockerclient.File) error {
	m.record("CopyToContainer", id, destDir, files)
	if m.CopyToContainerFunc != nil {
		return m.CopyToContainerFunc(ctx, id, destDir, files)
	}
	return nil
}

func (m *Mock) CopyArchiveToContainer(ctx context.Context, id, destDir string, archive io.Reader) error {
	m.record("CopyArchiveToContainer", id, destDir)
	if m.CopyArchiveToContainerFunc != nil {
		return m.CopyArchiveToContainerFunc(ctx, id, destDir, archive)
	}
	return nil
}

func (m *Mock) ExecCreate(ctx context.Context, containerID string, cfg dockerclient.ExecConfig) (string, error) {
	m.record("ExecCreate", containerID, cfg)
	if m.ExecCreateFunc != nil {
		return m.ExecCreateFunc(ctx, containerID, cfg)
	}
	return "", nil
}

func (m *Mock) ExecStartTo(ctx context.Context, execID string, stdout, stderr io.Writer) error {
	m.record("ExecStartTo", execID)
	if m.ExecStartToFunc != nil {
		return m.ExecStartToFunc(ctx, execID, stdout, stderr)
	}
	return nil
}

func (m *Mock) ExecInspect(ctx context.Context, execID string) (dockerclient.ExecInspect, error) {
	m.record("ExecInspect", execID)
	if m.ExecInspectFunc != nil {
		return m.ExecInspectFunc(ctx, execID)
	}
	return dockerclient.ExecInspect{}, nil
}

func (m *Mock) Exec(ctx context.Context, containerID string, cmd ...string) (dockerclient.ExecResult, error) {
	m.record("Exec", containerID, cmd)
	if m.ExecFunc != nil {
		return m.ExecFunc(ctx, containerID, cmd...)
	}
	return dockerclient.ExecResult{}, nil
}

// Mock is a Client.
var _ dockerclient.Client = (*Mock)(nil)
