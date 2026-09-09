package dockerclient_test

import (
	"errors"
	"io"
	"io/fs"
	"testing"

	"github.com/tophergopher/mongotest/dockerclient"
)

// The interface methods are measured by dockerclienttest.Benchmarks, which
// every implementation runs. What is measured here is the rest of the
// package: the validation and error helpers that sit on the hot path of
// every call an implementation makes.

func BenchmarkParseImageRef(b *testing.B) {
	b.ReportAllocs()
	for b.Loop() {
		if _, err := dockerclient.ParseImageRef("localhost:5000/team/mongo:8.0-noble"); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkCheckID(b *testing.B) {
	b.ReportAllocs()
	for b.Loop() {
		if _, err := dockerclient.CheckID("container", "de686f1e8de9bcabdc4b20b2d9b80d33f90ed79aa7588ad907d953ad63dc9d3c"); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkCheckContainerName(b *testing.B) {
	b.ReportAllocs()
	for b.Loop() {
		if _, err := dockerclient.CheckContainerName("mongotest-1a2b3c"); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkCheckImageRefOrID(b *testing.B) {
	b.ReportAllocs()
	for b.Loop() {
		if _, err := dockerclient.CheckImageRefOrID("sha256:41c3b7abb48e"); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkCheckPorts(b *testing.B) {
	b.Run("CheckPortKey", func(b *testing.B) {
		b.ReportAllocs()
		for b.Loop() {
			if err := dockerclient.CheckPortKey("ExposedPorts", "27017/tcp"); err != nil {
				b.Fatal(err)
			}
		}
	})
	b.Run("CheckHostPort", func(b *testing.B) {
		b.ReportAllocs()
		for b.Loop() {
			if err := dockerclient.CheckHostPort("PortBindings", "40000-40010"); err != nil {
				b.Fatal(err)
			}
		}
	})
}

func BenchmarkValidateFiles(b *testing.B) {
	files := []dockerclient.File{
		{Name: "mongo-tls/server.pem", Mode: 0o644, Content: make([]byte, 4096)},
		{Name: "mongo-tls/ca.pem", Mode: 0o644, Content: make([]byte, 2048)},
	}
	b.ReportAllocs()
	for b.Loop() {
		if err := dockerclient.ValidateFiles(files); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkContainerConfigValidate(b *testing.B) {
	cfg := dockerclient.ContainerConfig{
		Image:        "mongo:8",
		Cmd:          []string{"--replSet", "rs0"},
		Labels:       map[string]string{"mongotest": "regression"},
		ExposedPorts: map[string]struct{}{"27017/tcp": {}},
		HostConfig: &dockerclient.HostConfig{PortBindings: map[string][]dockerclient.PortBinding{
			"27017/tcp": {{HostIP: "127.0.0.1", HostPort: ""}},
		}},
	}
	b.ReportAllocs()
	for b.Loop() {
		if err := cfg.Validate(); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkExecConfigValidate(b *testing.B) {
	cfg := dockerclient.ExecConfig{Cmd: []string{"mongosh", "--quiet", "--eval", "1"}, WorkingDir: "/tmp"}
	b.ReportAllocs()
	for b.Loop() {
		if err := cfg.Validate("1.44"); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkContainerInspectHostPort(b *testing.B) {
	info := dockerclient.ContainerInspect{
		NetworkSettings: dockerclient.NetworkSettings{Ports: map[string][]dockerclient.PortBinding{
			"27017/tcp": {{HostIP: "127.0.0.1", HostPort: "34819"}},
		}},
	}
	b.ReportAllocs()
	for b.Loop() {
		if info.HostPort("27017/tcp") == "" {
			b.Fatal("expected a published port")
		}
	}
}

func BenchmarkNopLogger(b *testing.B) {
	b.Run("construct", func(b *testing.B) {
		b.ReportAllocs()
		for b.Loop() {
			_ = dockerclient.NopLogger()
		}
	})
	b.Run("log", func(b *testing.B) {
		l := dockerclient.NopLogger()
		b.ReportAllocs()
		for b.Loop() {
			l.Debug("docker request", "method", "GET", "path", "/version", "status", 200)
		}
	})
}

func BenchmarkErrorConstructors(b *testing.B) {
	b.Run("InvalidArgument", func(b *testing.B) {
		b.ReportAllocs()
		for b.Loop() {
			_ = dockerclient.InvalidArgument("image reference", "Mongo:8", "must be lowercase", "use a lowercase name")
		}
	})
	b.Run("ConnectionFailed", func(b *testing.B) {
		b.ReportAllocs()
		for b.Loop() {
			_ = dockerclient.ConnectionFailed("unix:///var/run/docker.sock", "refused", "start the daemon", nil)
		}
	})
	b.Run("DecodeError", func(b *testing.B) {
		b.ReportAllocs()
		for b.Loop() {
			_ = dockerclient.DecodeError("GET", "/containers/x/json", io.ErrUnexpectedEOF)
		}
	})
	b.Run("UnexpectedStatus", func(b *testing.B) {
		b.ReportAllocs()
		for b.Loop() {
			_ = dockerclient.UnexpectedStatus("POST", "/containers/create", 202)
		}
	})
	b.Run("WrapConnectionError", func(b *testing.B) {
		b.ReportAllocs()
		for b.Loop() {
			_ = dockerclient.WrapConnectionError("unix:///var/run/docker.sock", fs.ErrNotExist)
		}
	})
	b.Run("RootCause", func(b *testing.B) {
		err := errors.New("connection refused")
		b.ReportAllocs()
		for b.Loop() {
			_ = dockerclient.RootCause(err)
		}
	})
}

func BenchmarkErrorMessages(b *testing.B) {
	cases := map[string]error{
		"StatusError":          &dockerclient.StatusError{StatusCode: 404, Message: "No such container: x", Method: "GET", Path: "/containers/x/json"},
		"InvalidArgumentError": dockerclient.InvalidArgument("image reference", "Mongo:8", "must be lowercase", "use a lowercase name"),
		"ConnectionError":      dockerclient.ConnectionFailed("unix:///docker.sock", "refused", "start the daemon", nil),
		"APIVersionError":      &dockerclient.APIVersionError{Feature: "WorkingDir", Required: "1.35", Negotiated: "1.30"},
		"ResponseError":        dockerclient.DecodeError("GET", "/x", io.ErrUnexpectedEOF),
		"StreamError":          &dockerclient.StreamError{Problem: "reading a frame header", Err: io.ErrUnexpectedEOF},
		"PullError":            &dockerclient.PullError{Ref: "mongo:nope", Message: "manifest unknown"},
	}
	for name, err := range cases {
		b.Run(name, func(b *testing.B) {
			b.ReportAllocs()
			for b.Loop() {
				_ = err.Error()
			}
		})
	}
}

func BenchmarkErrorMatching(b *testing.B) {
	statusErr := &dockerclient.StatusError{StatusCode: 404, Message: "No such container: x", Method: "GET", Path: "/x"}
	cases := map[string]struct{ err, target error }{
		"IsNotFound":              {statusErr, dockerclient.ErrNotFound},
		"StatusError.Is":          {statusErr, dockerclient.ErrConflict},
		"InvalidArgumentError.Is": {dockerclient.ErrNoCommand, dockerclient.ErrInvalidArgument},
		"ConnectionError.Is":      {dockerclient.ConnectionFailed("h", "p", "f", nil), dockerclient.ErrConnectionFailed},
		"APIVersionError.Is":      {&dockerclient.APIVersionError{ServerMin: "1.60"}, dockerclient.ErrAPIVersion},
		"ResponseError.Is":        {dockerclient.DecodeError("GET", "/x", io.EOF), dockerclient.ErrDaemonResponse},
		"StreamError.Is":          {&dockerclient.StreamError{Problem: "x"}, dockerclient.ErrStream},
		"PullError.Is":            {&dockerclient.PullError{Ref: "mongo"}, dockerclient.ErrPull},
	}
	for name, tc := range cases {
		b.Run(name, func(b *testing.B) {
			b.ReportAllocs()
			for b.Loop() {
				_ = errors.Is(tc.err, tc.target)
			}
		})
	}
}

func BenchmarkIsNotFound(b *testing.B) {
	err := &dockerclient.StatusError{StatusCode: 404, Message: "No such container: x", Method: "GET", Path: "/x"}
	b.ReportAllocs()
	for b.Loop() {
		if !dockerclient.IsNotFound(err) {
			b.Fatal("expected a not-found error")
		}
	}
}

func BenchmarkStatusErrorHint(b *testing.B) {
	err := &dockerclient.StatusError{StatusCode: 409, Message: "in use", Method: "DELETE", Path: "/containers/x"}
	b.ReportAllocs()
	for b.Loop() {
		if err.Hint() == "" {
			b.Fatal("expected a hint for 409")
		}
	}
}

func BenchmarkErrorUnwrap(b *testing.B) {
	cases := map[string]error{
		"ConnectionError": dockerclient.ConnectionFailed("h", "p", "f", io.EOF),
		"ResponseError":   dockerclient.DecodeError("GET", "/x", io.EOF),
		"StreamError":     &dockerclient.StreamError{Problem: "p", Err: io.EOF},
		"PullError":       &dockerclient.PullError{Ref: "mongo", Message: "m", Err: io.EOF},
	}
	for name, err := range cases {
		b.Run(name, func(b *testing.B) {
			b.ReportAllocs()
			for b.Loop() {
				_ = errors.Unwrap(err)
			}
		})
	}
}
