package dockermock

import (
	"archive/tar"
	"context"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
	"sync"

	"github.com/tophergopher/mongotest/dockerclient"
)

// Fake is a dockerclient.Client backed by an in-memory image and container
// store. It behaves like a daemon rather than replaying a script: an image
// has to be pulled before a container can be created from it, a container
// has to be started before it is running, and a removed container is gone.
//
// It applies the same validation a real client does, so a bad image
// reference or port binding fails in a test exactly as it would in
// production.
//
// It is safe for concurrent use.
type Fake struct {
	mu         sync.Mutex
	images     map[string]dockerclient.ImageInspect
	containers map[string]*fakeContainer
	execs      map[string]*fakeExec
	nextID     int
	nextPort   int

	// ExecFunc decides what a command run inside a container produces. The
	// default reports an empty stdout and exit code 0. Set it to simulate a
	// command's output, or a failure.
	ExecFunc func(containerID string, cmd []string) dockerclient.ExecResult
	// PullFunc decides the outcome of an image pull. The default records the
	// image as present and succeeds. Return an error to simulate a registry
	// failure; the image is then not recorded.
	PullFunc func(ref string) error
	// Processes decides what ContainerTop reports for a running container.
	// The default reports one row per container command, which is enough for
	// code that waits for a named process to appear.
	Processes func(c ContainerState) [][]string
}

// ContainerState is what the Fake knows about one container, passed to the
// Processes hook and returned by Containers.
type ContainerState struct {
	ID      string
	Name    string
	Config  dockerclient.ContainerConfig
	Running bool
	Ports   map[string][]dockerclient.PortBinding
	// Files holds everything copied into the container, keyed by the path it
	// would have inside it, so a test can assert what was placed where.
	Files map[string][]byte
}

type fakeContainer struct {
	state ContainerState
}

type fakeExec struct {
	containerID string
	cfg         dockerclient.ExecConfig
	result      dockerclient.ExecResult
	started     bool
}

// FakeOption configures a Fake.
type FakeOption func(*Fake)

// WithImages marks images as already pulled, so a test can create containers
// without pulling first.
func WithImages(refs ...string) FakeOption {
	return func(f *Fake) {
		for _, ref := range refs {
			f.images[ref] = dockerclient.ImageInspect{
				ID: "sha256:" + strings.Repeat("a", 12), RepoTags: []string{ref}, Architecture: "amd64", OS: "linux",
			}
		}
	}
}

// NewFake returns a Fake with an empty store.
func NewFake(opts ...FakeOption) *Fake {
	f := &Fake{
		images:     map[string]dockerclient.ImageInspect{},
		containers: map[string]*fakeContainer{},
		execs:      map[string]*fakeExec{},
		nextPort:   32768,
	}
	for _, opt := range opts {
		opt(f)
	}
	return f
}

// Containers returns the state of every container the Fake still holds, so a
// test can assert that nothing was left behind.
func (f *Fake) Containers() []ContainerState {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]ContainerState, 0, len(f.containers))
	for _, c := range f.containers {
		out = append(out, c.state)
	}
	return out
}

// Host reports a placeholder address; nothing is dialled.
func (f *Fake) Host() string { return "fake://dockermock" }

func (f *Fake) ImagePull(ctx context.Context, ref string) error {
	parsed, err := dockerclient.ParseImageRef(ref)
	if err != nil {
		return err
	}
	if f.PullFunc != nil {
		if err := f.PullFunc(ref); err != nil {
			return err
		}
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.images[ref] = dockerclient.ImageInspect{
		ID:           "sha256:" + strings.Repeat("a", 12),
		RepoTags:     []string{parsed.Name + ":" + parsed.Tag},
		Architecture: "amd64",
		OS:           "linux",
	}
	return nil
}

func (f *Fake) ImageInspect(ctx context.Context, ref string) (dockerclient.ImageInspect, error) {
	if _, err := dockerclient.CheckImageRefOrID(ref); err != nil {
		return dockerclient.ImageInspect{}, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	img, ok := f.images[ref]
	if !ok {
		return dockerclient.ImageInspect{}, notFound("image", ref, "GET", "/images/"+ref+"/json")
	}
	return img, nil
}

func (f *Fake) ContainerCreate(ctx context.Context, name string, cfg dockerclient.ContainerConfig) (string, []string, error) {
	if err := cfg.Validate(); err != nil {
		return "", nil, err
	}
	name, err := dockerclient.CheckContainerName(name)
	if err != nil {
		return "", nil, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if _, ok := f.images[cfg.Image]; !ok {
		// Same as a daemon: the caller is expected to pull and retry.
		return "", nil, notFound("image", cfg.Image, "POST", "/containers/create")
	}
	f.nextID++
	id := fmt.Sprintf("fake%08d", f.nextID)
	if name == "" {
		name = "dockermock-" + id
	}
	ports := map[string][]dockerclient.PortBinding{}
	if cfg.HostConfig != nil {
		for port, bindings := range cfg.HostConfig.PortBindings {
			for _, b := range bindings {
				if b.HostPort == "" {
					// Assign one, as the daemon does for an empty HostPort.
					f.nextPort++
					b.HostPort = strconv.Itoa(f.nextPort)
				}
				if b.HostIP == "" {
					b.HostIP = "0.0.0.0"
				}
				ports[port] = append(ports[port], b)
			}
		}
	}
	f.containers[id] = &fakeContainer{state: ContainerState{
		ID: id, Name: "/" + name, Config: cfg, Ports: ports, Files: map[string][]byte{},
	}}
	return id, nil, nil
}

// container looks up a container, returning the daemon's not-found error.
func (f *Fake) container(id, method, path string) (*fakeContainer, error) {
	clean, err := dockerclient.CheckID("container", id)
	if err != nil {
		return nil, err
	}
	c, ok := f.containers[clean]
	if !ok {
		return nil, notFound("container", clean, method, path)
	}
	return c, nil
}

func (f *Fake) ContainerStart(ctx context.Context, id string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	c, err := f.container(id, "POST", "/containers/"+id+"/start")
	if err != nil {
		return err
	}
	c.state.Running = true // starting an already running container is not an error
	return nil
}

func (f *Fake) ContainerRemove(ctx context.Context, id string, opts dockerclient.RemoveOptions) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	c, err := f.container(id, "DELETE", "/containers/"+id)
	if err != nil {
		return err
	}
	if c.state.Running && !opts.Force {
		return &dockerclient.StatusError{
			StatusCode: 409, Method: "DELETE", Path: "/containers/" + id,
			Message: "You cannot remove a running container " + c.state.ID + ". Stop the container before attempting removal or force remove",
		}
	}
	delete(f.containers, c.state.ID)
	return nil
}

func (f *Fake) ContainerInspect(ctx context.Context, id string) (dockerclient.ContainerInspect, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	c, err := f.container(id, "GET", "/containers/"+id+"/json")
	if err != nil {
		return dockerclient.ContainerInspect{}, err
	}
	status := "created"
	if c.state.Running {
		status = "running"
	}
	return dockerclient.ContainerInspect{
		ID:              c.state.ID,
		Name:            c.state.Name,
		State:           dockerclient.ContainerState{Status: status, Running: c.state.Running},
		Config:          dockerclient.InspectedConfig{Image: c.state.Config.Image, Labels: c.state.Config.Labels},
		NetworkSettings: dockerclient.NetworkSettings{Ports: c.state.Ports},
	}, nil
}

func (f *Fake) ContainerTop(ctx context.Context, id string) (dockerclient.Top, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	c, err := f.container(id, "GET", "/containers/"+id+"/top")
	if err != nil {
		return dockerclient.Top{}, err
	}
	if !c.state.Running {
		return dockerclient.Top{}, &dockerclient.StatusError{
			StatusCode: 409, Method: "GET", Path: "/containers/" + id + "/top",
			Message: "Container " + c.state.ID + " is not running",
		}
	}
	if f.Processes != nil {
		return dockerclient.Top{Titles: []string{"PID", "CMD"}, Processes: f.Processes(c.state)}, nil
	}
	cmd := append([]string{"entrypoint"}, c.state.Config.Cmd...)
	return dockerclient.Top{
		Titles:    []string{"PID", "CMD"},
		Processes: [][]string{{"1", strings.Join(cmd, " ")}},
	}, nil
}

func (f *Fake) CopyToContainer(ctx context.Context, id, destDir string, files []dockerclient.File) error {
	if err := dockerclient.ValidateFiles(files); err != nil {
		return err
	}
	if !strings.HasPrefix(destDir, "/") {
		return dockerclient.InvalidArgument("destination directory", destDir,
			"it must be an absolute path inside the container", `use a path like "/etc/mongo-tls" or "/tmp"`)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	c, err := f.container(id, "PUT", "/containers/"+id+"/archive")
	if err != nil {
		return err
	}
	for _, file := range files {
		c.state.Files[strings.TrimSuffix(destDir, "/")+"/"+file.Name] = file.Content
	}
	return nil
}

func (f *Fake) CopyArchiveToContainer(ctx context.Context, id, destDir string, archive io.Reader) error {
	if archive == nil {
		return dockerclient.InvalidArgument("archive", "", "no tar stream was given",
			"pass an io.Reader that yields a tar archive")
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	c, err := f.container(id, "PUT", "/containers/"+id+"/archive")
	if err != nil {
		return err
	}
	tr := tar.NewReader(archive)
	for {
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return &dockerclient.StreamError{Problem: "reading the tar archive", Err: err}
		}
		body, err := io.ReadAll(tr)
		if err != nil {
			return &dockerclient.StreamError{Problem: "reading tar entry " + hdr.Name, Err: err}
		}
		c.state.Files[strings.TrimSuffix(destDir, "/")+"/"+hdr.Name] = body
	}
}

func (f *Fake) ExecCreate(ctx context.Context, containerID string, cfg dockerclient.ExecConfig) (string, error) {
	// The Fake reports the newest API version, so no feature gate applies.
	if err := cfg.Validate("1.44"); err != nil {
		return "", err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	c, err := f.container(containerID, "POST", "/containers/"+containerID+"/exec")
	if err != nil {
		return "", err
	}
	f.nextID++
	execID := fmt.Sprintf("exec%08d", f.nextID)
	result := dockerclient.ExecResult{}
	if f.ExecFunc != nil {
		result = f.ExecFunc(c.state.ID, cfg.Cmd)
	}
	f.execs[execID] = &fakeExec{containerID: c.state.ID, cfg: cfg, result: result}
	return execID, nil
}

func (f *Fake) ExecStartTo(ctx context.Context, execID string, stdout, stderr io.Writer) error {
	f.mu.Lock()
	e, ok := f.execs[execID]
	f.mu.Unlock()
	if !ok {
		return notFound("exec instance", execID, "POST", "/exec/"+execID+"/start")
	}
	if stdout == nil {
		stdout = io.Discard
	}
	if stderr == nil {
		stderr = io.Discard
	}
	if _, err := io.WriteString(stdout, e.result.Stdout); err != nil {
		return &dockerclient.StreamError{Problem: "writing exec stdout", Err: err}
	}
	if _, err := io.WriteString(stderr, e.result.Stderr); err != nil {
		return &dockerclient.StreamError{Problem: "writing exec stderr", Err: err}
	}
	f.mu.Lock()
	e.started = true
	f.mu.Unlock()
	return nil
}

func (f *Fake) ExecInspect(ctx context.Context, execID string) (dockerclient.ExecInspect, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	e, ok := f.execs[execID]
	if !ok {
		return dockerclient.ExecInspect{}, notFound("exec instance", execID, "GET", "/exec/"+execID+"/json")
	}
	return dockerclient.ExecInspect{
		ID:          execID,
		Running:     !e.started,
		ExitCode:    e.result.ExitCode,
		ContainerID: e.containerID,
	}, nil
}

func (f *Fake) Exec(ctx context.Context, containerID string, cmd ...string) (dockerclient.ExecResult, error) {
	if len(cmd) == 0 {
		return dockerclient.ExecResult{}, dockerclient.ErrNoCommand
	}
	execID, err := f.ExecCreate(ctx, containerID, dockerclient.ExecConfig{Cmd: cmd})
	if err != nil {
		return dockerclient.ExecResult{}, err
	}
	var stdout, stderr strings.Builder
	if err := f.ExecStartTo(ctx, execID, &stdout, &stderr); err != nil {
		return dockerclient.ExecResult{}, err
	}
	ins, err := f.ExecInspect(ctx, execID)
	if err != nil {
		return dockerclient.ExecResult{}, err
	}
	return dockerclient.ExecResult{Stdout: stdout.String(), Stderr: stderr.String(), ExitCode: ins.ExitCode}, nil
}

// notFound builds the daemon's 404 for a missing object.
func notFound(kind, id, method, path string) error {
	return &dockerclient.StatusError{
		StatusCode: 404,
		Message:    "No such " + kind + ": " + id,
		Method:     method,
		Path:       path,
	}
}

// Fake is a Client.
var _ dockerclient.Client = (*Fake)(nil)
