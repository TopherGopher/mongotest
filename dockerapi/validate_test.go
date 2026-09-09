package dockerapi

import (
	"context"
	"errors"
	"io/fs"
	"net"
	"net/http"
	"strings"
	"testing"

	"github.com/tophergopher/mongotest/internal/fakedaemon"
)

func TestParseImageRef(t *testing.T) {
	good := []struct{ ref, name, tag, digest string }{
		{"mongo", "mongo", "latest", ""},
		{"mongo:8", "mongo", "8", ""},
		{"mongo:8.0-noble", "mongo", "8.0-noble", ""},
		{"library/mongo:8", "library/mongo", "8", ""},
		{"docker.io/library/mongo:8", "docker.io/library/mongo", "8", ""},
		{"localhost:5000/mongo", "localhost:5000/mongo", "latest", ""},
		{"localhost:5000/team/mongo:8.0", "localhost:5000/team/mongo", "8.0", ""},
		{"GHCR.io/org-name/img:v1", "GHCR.io/org-name/img", "v1", ""}, // uppercase allowed in the registry host only
		{"mongo:8/json", "mongo:8/json", "latest", ""},                // legal: host "mongo:8", repository "json"
		{"[::1]:5000/mongo", "[::1]:5000/mongo", "latest", ""},
		{"mongo@sha256:" + strings.Repeat("a", 64), "mongo", "", "sha256:" + strings.Repeat("a", 64)},
		{"mongo:8@sha256:" + strings.Repeat("0", 64), "mongo", "8", "sha256:" + strings.Repeat("0", 64)},
		{"my__image.name-x", "my__image.name-x", "latest", ""},
	}
	for _, tc := range good {
		r, err := parseImageRef(tc.ref)
		if err != nil {
			t.Errorf("parseImageRef(%q): %v", tc.ref, err)
			continue
		}
		if r.name != tc.name || r.tag != tc.tag || r.digest != tc.digest {
			t.Errorf("parseImageRef(%q) = %+v", tc.ref, r)
		}
	}
	bad := []string{
		"", " ", "Mongo", "mongo:8:9", "mongo:", ":8", "mongo/", "/mongo", "mongo//x", "mongo:8 ", "mongo:-8",
		"mongo@sha256:short", "mongo@notadigest", "mon go", "mongo:" + strings.Repeat("t", 129),
		strings.Repeat("a", 256), "mongo?x=1", "mongo#1", "ghcr.io/Org-Name/img:v1",
	}
	for _, ref := range bad {
		if _, err := parseImageRef(ref); err == nil {
			t.Errorf("parseImageRef(%q) should fail", ref)
		}
	}
	// A lowercase violation gets the daemon's familiar wording.
	if _, err := parseImageRef("Mongo:8"); err == nil || !strings.Contains(err.Error(), "lowercase") {
		t.Errorf("uppercase error = %v", err)
	}
}

func TestCheckID(t *testing.T) {
	for _, in := range []string{"abc123", "  abc123 ", "/mongotest-1", "mongotest_1.x-y", "de686f1e8de9bcabdc4b20b2d9b80d33f90ed79aa7588ad907d953ad63dc9d3c"} {
		got, err := checkID("container", in)
		if err != nil || got == "" || strings.ContainsAny(got, " /") {
			t.Errorf("checkID(%q) = %q, %v", in, got, err)
		}
	}
	for _, in := range []string{"", "   ", "a/b", "../x", "abc?force=1", "abc#", "-abc", ".abc", "a b", "a\nb"} {
		if _, err := checkID("container", in); err == nil {
			t.Errorf("checkID(%q) should fail", in)
		}
	}
	_, err := checkID("exec", "")
	if err == nil || !strings.Contains(err.Error(), "exec") {
		t.Errorf("empty id error should name the object type: %v", err)
	}
}

func TestValidateContainerConfig(t *testing.T) {
	ok := ContainerConfig{
		Image:        "mongo:8",
		Labels:       map[string]string{"mongotest": "regression"},
		ExposedPorts: map[string]struct{}{"27017/tcp": {}, "27018": {}},
		HostConfig: &HostConfig{PortBindings: map[string][]PortBinding{
			"27017/tcp": {{HostIP: "127.0.0.1", HostPort: "34819"}, {HostIP: "", HostPort: ""}, {HostPort: "40000-40010"}},
			"53/udp":    {{HostIP: "::1", HostPort: "5353"}},
		}},
	}
	if err := ok.validate(); err != nil {
		t.Fatalf("valid config rejected: %v", err)
	}
	bad := map[string]ContainerConfig{
		"no image":          {},
		"bad image":         {Image: "Mongo"},
		"empty label key":   {Image: "mongo", Labels: map[string]string{"": "x"}},
		"exposed no number": {Image: "mongo", ExposedPorts: map[string]struct{}{"tcp": {}}},
		"exposed port zero": {Image: "mongo", ExposedPorts: map[string]struct{}{"0/tcp": {}}},
		"exposed too big":   {Image: "mongo", ExposedPorts: map[string]struct{}{"70000/tcp": {}}},
		"bad proto":         {Image: "mongo", ExposedPorts: map[string]struct{}{"27017/icmp": {}}},
		"binding key":       {Image: "mongo", HostConfig: &HostConfig{PortBindings: map[string][]PortBinding{"x/tcp": {{}}}}},
		"binding host ip":   {Image: "mongo", HostConfig: &HostConfig{PortBindings: map[string][]PortBinding{"27017/tcp": {{HostIP: "localhost"}}}}},
		"binding host port": {Image: "mongo", HostConfig: &HostConfig{PortBindings: map[string][]PortBinding{"27017/tcp": {{HostPort: "abc"}}}}},
		"binding range":     {Image: "mongo", HostConfig: &HostConfig{PortBindings: map[string][]PortBinding{"27017/tcp": {{HostPort: "40010-40000"}}}}},
		"empty cmd element": {Image: "mongo", Cmd: []string{"--replSet", ""}},
	}
	for name, cfg := range bad {
		if err := cfg.validate(); err == nil {
			t.Errorf("%s: expected validation error", name)
		}
	}
}

func TestContainerCreateRejectsBadInputBeforeRequest(t *testing.T) {
	c := newMockClient(t, noRequest(t))
	if _, _, err := c.ContainerCreate(context.Background(), "", ContainerConfig{}); err == nil {
		t.Fatal("empty image accepted")
	}
	if _, _, err := c.ContainerCreate(context.Background(), "bad name!", ContainerConfig{Image: "mongo:8"}); err == nil {
		t.Fatal("invalid container name accepted")
	}
	if _, _, err := c.ContainerCreate(context.Background(), "x", ContainerConfig{Image: "mongo:8"}); err == nil {
		t.Fatal("one-character name accepted (daemon requires at least two)")
	}
}

func TestIDGuardsOnEveryContainerEndpoint(t *testing.T) {
	c := newMockClient(t, noRequest(t))
	ctx := context.Background()
	calls := map[string]func() error{
		"start":   func() error { return c.ContainerStart(ctx, " ") },
		"remove":  func() error { return c.ContainerRemove(ctx, "a/b", RemoveOptions{}) },
		"inspect": func() error { _, err := c.ContainerInspect(ctx, ""); return err },
		"top":     func() error { _, err := c.ContainerTop(ctx, "?"); return err },
		"copy":    func() error { return c.CopyToContainer(ctx, "", "/tmp", []File{{Name: "x"}}) },
		"exec create": func() error {
			_, err := c.ExecCreate(ctx, "../x", ExecConfig{Cmd: []string{"true"}})
			return err
		},
		"exec start":   func() error { _, _, err := c.ExecStart(ctx, ""); return err },
		"exec inspect": func() error { _, err := c.ExecInspect(ctx, "a b"); return err },
		"exec":         func() error { _, err := c.Exec(ctx, "", "true"); return err },
		"image inspect": func() error {
			_, err := c.ImageInspect(ctx, "mongo:8?x=1")
			return err
		},
		"image pull": func() error { return c.ImagePull(ctx, "Mongo") },
	}
	for name, call := range calls {
		if err := call(); err == nil {
			t.Errorf("%s: invalid id accepted", name)
		}
	}
}

func TestImageInspectAcceptsImageIDs(t *testing.T) {
	c := newMockClient(t, func(req *http.Request) (*http.Response, error) {
		return mockJSON(200, map[string]string{"Id": "x"})(req)
	})
	for _, id := range []string{"sha256:" + strings.Repeat("ab", 32), "41c3b7abb48e"} {
		if _, err := c.ImageInspect(context.Background(), id); err != nil {
			t.Errorf("image id %q rejected: %v", id, err)
		}
	}
}

func TestExecCreateGuards(t *testing.T) {
	c := newMockClient(t, mockJSON(http.StatusCreated, map[string]string{"Id": "e"}))
	ctx := context.Background()
	if _, err := c.ExecCreate(ctx, "abc", ExecConfig{}); err == nil {
		t.Error("empty Cmd accepted")
	}
	if _, err := c.ExecCreate(ctx, "abc", ExecConfig{Cmd: []string{""}}); err == nil {
		t.Error("empty program accepted")
	}
	if _, err := c.ExecCreate(ctx, "abc", ExecConfig{Cmd: []string{"sh"}, Env: []string{"NOEQUALS"}}); err == nil {
		t.Error("malformed env accepted")
	}
	if _, err := c.ExecCreate(ctx, "abc", ExecConfig{Cmd: []string{"sh"}, WorkingDir: "relative"}); err == nil {
		t.Error("relative working dir accepted")
	}
	if _, err := c.ExecCreate(ctx, "abc", ExecConfig{Cmd: []string{"sh"}, Env: []string{"A=1"}, WorkingDir: "/tmp"}); err != nil {
		t.Errorf("valid exec rejected: %v", err)
	}
}

func TestExecCreateVersionGates(t *testing.T) {
	c := newMockClient(t, mockJSON(http.StatusCreated, map[string]string{"Id": "e"}), WithAPIVersion("1.30"))
	ctx := context.Background()
	// Env needs 1.25, satisfied by 1.30.
	if _, err := c.ExecCreate(ctx, "abc", ExecConfig{Cmd: []string{"sh"}, Env: []string{"A=1"}}); err != nil {
		t.Fatalf("Env on 1.30: %v", err)
	}
	// WorkingDir needs 1.35.
	_, err := c.ExecCreate(ctx, "abc", ExecConfig{Cmd: []string{"sh"}, WorkingDir: "/tmp"})
	if err == nil || !strings.Contains(err.Error(), "1.35") || !strings.Contains(err.Error(), "1.30") {
		t.Fatalf("WorkingDir on 1.30 should be rejected naming both versions, got %v", err)
	}
}

func TestFileModeValidation(t *testing.T) {
	if _, err := buildTar([]File{{Name: "x", Mode: fs.ModeDir | 0o755}}); err == nil {
		t.Error("type bits in Mode must be rejected")
	}
	if _, err := buildTar([]File{{Name: "x", Mode: fs.ModeSetuid | 0o755}}); err == nil {
		t.Error("setuid bit must be rejected")
	}
	if _, err := buildTar([]File{{Name: "x", Mode: 0o1777}}); err == nil {
		t.Error("sticky bit expressed as an octal literal must be rejected")
	}
	if _, err := buildTar([]File{{Name: "x", Mode: 0o600}}); err != nil {
		t.Errorf("plain permission bits rejected: %v", err)
	}
}

func TestCopyToContainerDestMustBeAbsolute(t *testing.T) {
	c := newMockClient(t, noRequest(t))
	for _, dest := range []string{"", "tmp", "./tmp", "../etc"} {
		if err := c.CopyToContainer(context.Background(), "abc", dest, []File{{Name: "x"}}); err == nil {
			t.Errorf("destDir %q accepted", dest)
		}
	}
}

func TestConnectionErrorsAreActionable(t *testing.T) {
	c, err := New(WithHost("unix:///nonexistent/dir/docker.sock"))
	if err != nil {
		t.Fatal(err)
	}
	err = c.Negotiate(context.Background())
	if !errors.Is(err, ErrConnectionFailed) {
		t.Fatalf("want ErrConnectionFailed, got %v", err)
	}
	for _, want := range []string{"/nonexistent/dir/docker.sock", "daemon running"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q should mention %q", err, want)
		}
	}
	if strings.Contains(err.Error(), "api.moby.localhost") {
		t.Errorf("placeholder host leaks into the message: %v", err)
	}

	// Context errors pass through undecorated so callers can compare them.
	fd := fakedaemon.NewTCP(t)
	c2, _ := New(WithHost(fd.Host()))
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := c2.Negotiate(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("context errors must pass through undecorated, got %v", err)
	}

	// Connection refused on TCP.
	l, _ := net.Listen("tcp", "127.0.0.1:0")
	addr := l.Addr().String()
	l.Close()
	c3, _ := New(WithHost("tcp://" + addr))
	err = c3.Negotiate(context.Background())
	if !errors.Is(err, ErrConnectionFailed) || !strings.Contains(err.Error(), addr) {
		t.Fatalf("refused: %v", err)
	}
}

func TestPermissionDeniedSocketMessage(t *testing.T) {
	err := wrapConnError("unix:///var/run/docker.sock", fs.ErrPermission)
	if !errors.Is(err, ErrConnectionFailed) || !strings.Contains(err.Error(), "permission denied") || !strings.Contains(err.Error(), "docker group") {
		t.Fatalf("err = %v", err)
	}
}
