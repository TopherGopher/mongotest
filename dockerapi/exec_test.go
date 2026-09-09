package dockerapi

import (
	"bufio"
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"io"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tophergopher/mongotest/internal/fakedaemon"
)

// frame builds one frame of the daemon's multiplexed attach stream. When a
// container has no TTY, the daemon interleaves stdout and stderr on a single
// connection and marks each chunk with an 8-byte header: byte 0 is the
// stream (1 stdout, 2 stderr, 3 daemon error), bytes 1 to 3 are zero and
// bytes 4 to 7 hold the payload length as a big-endian uint32. Tests need to
// produce that exact byte layout to prove the client's demultiplexer reads
// it correctly, including when a frame is split across writes.
func frame(stream byte, payload string) []byte {
	hdr := make([]byte, 8)
	hdr[0] = stream
	binary.BigEndian.PutUint32(hdr[4:], uint32(len(payload)))
	return append(hdr, payload...)
}

// hijackHandler is an http.HandlerFunc that takes over the raw connection.
// POST /exec/{id}/start is not an ordinary request: the daemon answers
// "101 Switching Protocols" and then uses the same TCP or unix connection as
// a bidirectional byte stream for the process output. net/http's normal
// ResponseWriter cannot express that, so the fake must call Hijack to get
// the underlying net.Conn, write the 101 response line and headers by hand,
// and then hand the connection to fn, which writes stream frames directly.
func hijackHandler(t testing.TB, fn func(conn net.Conn, rw *bufio.ReadWriter)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Upgrade") != "tcp" || r.Header.Get("Connection") != "Upgrade" {
			fakedaemon.Error(w, 400, "missing upgrade headers")
			return
		}
		hj, ok := w.(http.Hijacker)
		if !ok {
			t.Error("the fake daemon's ResponseWriter must support Hijack to emulate exec start")
			return
		}
		conn, rw, err := hj.Hijack()
		if err != nil {
			t.Errorf("hijacking the connection: %v", err)
			return
		}
		defer conn.Close()
		rw.WriteString("HTTP/1.1 101 UPGRADED\r\nContent-Type: application/vnd.docker.multiplexed-stream\r\nConnection: Upgrade\r\nUpgrade: tcp\r\n\r\n")
		rw.Flush()
		fn(conn, rw)
	}
}

func TestExecCreate(t *testing.T) {
	fd, c := newImageClient(t)
	fd.Handle("POST", "/containers/{id}/exec", func(w http.ResponseWriter, r *http.Request) {
		fakedaemon.JSON(w, 201, execCreateResponse{ID: "exec-1"})
	})
	id, err := c.ExecCreate(context.Background(), "abc", ExecConfig{Cmd: []string{"mongosh", "--quiet", "--eval", "1"}, Env: []string{"A=1"}, WorkingDir: "/tmp"})
	require.NoError(t, err, "exec create against the fake daemon")
	assert.Equal(t, "exec-1", id, "the exec id from the daemon is returned")
	r := fd.Requests()[1]
	assert.Equal(t, "/containers/abc/exec", r.Path, "exec create hits /containers/{id}/exec")
	var sent execCreateRequest
	require.NoError(t, decodeBodyBytes(r.Body, &sent), "the exec create body must be JSON in the request shape")
	assert.True(t, sent.AttachStdout, "stdout is always attached")
	assert.True(t, sent.AttachStderr, "stderr is always attached")
	assert.False(t, sent.Tty, "a TTY is never requested so the streams stay separable")
	assert.Equal(t, []string{"mongosh", "--quiet", "--eval", "1"}, sent.Cmd, "Cmd is sent in order")
	assert.Equal(t, []string{"A=1"}, sent.Env, "Env is sent")
	assert.Equal(t, "/tmp", sent.WorkingDir, "WorkingDir is sent")
}

func execStartServer(t testing.TB, fd *fakedaemon.Server, fn func(conn net.Conn, rw *bufio.ReadWriter)) {
	fd.Handle("POST", "/exec/{id}/start", hijackHandler(t, fn))
}

func TestExecStartDemuxesSplitFrames(t *testing.T) {
	for _, mode := range []string{"unix", "tcp"} {
		t.Run(mode, func(t *testing.T) {
			var fd *fakedaemon.Server
			if mode == "unix" {
				fd = fakedaemon.New(t)
			} else {
				fd = fakedaemon.NewTCP(t)
			}
			fd.ServeVersion("1.54", "1.40")
			c, err := New(WithHost(fd.Host()))
			require.NoError(t, err, "client construction over %s", mode)
			execStartServer(t, fd, func(conn net.Conn, rw *bufio.ReadWriter) {
				rw.Write(frame(1, "hello "))
				rw.Flush()
				f := frame(2, "warn")
				rw.Write(f[:3]) // split the header across two writes
				rw.Flush()
				time.Sleep(20 * time.Millisecond)
				rw.Write(f[3:])
				rw.Write(frame(1, "world\n"))
				rw.Flush()
			})
			stdout, stderr, err := c.ExecStart(context.Background(), "exec-1")
			require.NoError(t, err, "exec start over %s", mode)
			assert.Equal(t, "hello world\n", string(stdout), "stdout frames must be reassembled in order even when a header is split across reads")
			assert.Equal(t, "warn", string(stderr), "stderr frames must land in the stderr buffer")
			r := fd.Requests()[1]
			assert.Equal(t, "/v1.44/exec/exec-1/start", r.RawPath, "exec start hits /exec/{id}/start under the negotiated version")
			assert.JSONEq(t, execStartBody, string(r.Body), "the start body asks for an attached, non-TTY run")
		})
	}
}

func TestExecStartToStreamsIntoWriters(t *testing.T) {
	fd, c := newImageClient(t)
	release := make(chan struct{})
	execStartServer(t, fd, func(conn net.Conn, rw *bufio.ReadWriter) {
		rw.Write(frame(1, "first\n"))
		rw.Flush()
		<-release
		rw.Write(frame(2, "second\n"))
		rw.Flush()
	})
	var out, errOut syncBuffer
	done := make(chan error, 1)
	go func() { done <- c.ExecStartTo(context.Background(), "e", &out, &errOut) }()
	require.Eventually(t, func() bool { return out.String() == "first\n" }, 2*time.Second, 10*time.Millisecond,
		"the first frame must reach the stdout writer before the stream ends; output is streamed, not buffered until EOF")
	close(release)
	require.NoError(t, <-done, "exec start to writers")
	assert.Equal(t, "second\n", errOut.String(), "the stderr writer receives stderr frames")
}

// syncBuffer is a goroutine-safe bytes.Buffer for streaming assertions.
type syncBuffer struct {
	mu  syncMutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

func TestExecStartLargeFrame(t *testing.T) {
	fd, c := newImageClient(t)
	big := strings.Repeat("x", 300*1024)
	execStartServer(t, fd, func(conn net.Conn, rw *bufio.ReadWriter) {
		rw.Write(frame(1, big))
		rw.Flush()
	})
	stdout, _, err := c.ExecStart(context.Background(), "e")
	require.NoError(t, err, "a 300 KiB frame must be read")
	assert.Len(t, stdout, len(big), "the whole payload must arrive")
}

func TestExecStartUnknownStreamType(t *testing.T) {
	fd, c := newImageClient(t)
	execStartServer(t, fd, func(conn net.Conn, rw *bufio.ReadWriter) {
		rw.Write(frame(7, "??"))
		rw.Flush()
	})
	_, _, err := c.ExecStart(context.Background(), "e")
	require.ErrorIs(t, err, ErrStream, "an unknown stream type is a stream error")
	assert.Contains(t, err.Error(), "stream type 7", "the error names the offending type")
}

func TestExecStartDaemonErrorFrame(t *testing.T) {
	fd, c := newImageClient(t)
	execStartServer(t, fd, func(conn net.Conn, rw *bufio.ReadWriter) {
		rw.Write(frame(1, "partial"))
		rw.Write(frame(3, "container abc is not running"))
		rw.Flush()
	})
	stdout, _, err := c.ExecStart(context.Background(), "e")
	require.ErrorIs(t, err, ErrStream, "a daemon error frame is a stream error")
	assert.Contains(t, err.Error(), "container abc is not running", "the daemon's message is preserved")
	assert.Equal(t, "partial", string(stdout), "output received before the error frame must be kept")
}

func TestExecStartTruncatedFrame(t *testing.T) {
	fd, c := newImageClient(t)
	execStartServer(t, fd, func(conn net.Conn, rw *bufio.ReadWriter) {
		f := frame(1, "hello")
		rw.Write(f[:10]) // header plus two payload bytes, then EOF
		rw.Flush()
	})
	_, _, err := c.ExecStart(context.Background(), "e")
	require.ErrorIs(t, err, ErrStream, "a stream cut mid-frame is a stream error")
	assert.ErrorIs(t, err, io.ErrUnexpectedEOF, "the underlying cause is an unexpected EOF")
}

func TestExecStartNonUpgradeResponse(t *testing.T) {
	fd, c := newImageClient(t)
	fd.Handle("POST", "/exec/{id}/start", func(w http.ResponseWriter, r *http.Request) {
		fakedaemon.Error(w, 404, "No such exec instance: e")
	})
	_, _, err := c.ExecStart(context.Background(), "e")
	assert.True(t, IsNotFound(err), "a 404 instead of an upgrade must be ErrNotFound, got %v", err)
	assert.Contains(t, err.Error(), "No such exec instance", "the daemon's message is preserved")
}

func TestExecStart200RawStreamIsAccepted(t *testing.T) {
	// Older daemons answer 200 with the stream as the body instead of 101,
	// and label it raw-stream even though it is framed.
	fd, c := newImageClient(t)
	fd.Handle("POST", "/exec/{id}/start", func(w http.ResponseWriter, r *http.Request) {
		hj := w.(http.Hijacker)
		conn, rw, _ := hj.Hijack()
		defer conn.Close()
		rw.WriteString("HTTP/1.1 200 OK\r\nContent-Type: application/vnd.docker.raw-stream\r\n\r\n")
		rw.Write(frame(1, "ok"))
		rw.Flush()
	})
	stdout, _, err := c.ExecStart(context.Background(), "e")
	require.NoError(t, err, "a 200 answer with a framed body must be accepted")
	assert.Equal(t, "ok", string(stdout), "the body is demultiplexed like a 101 stream")
}

func TestExecStartContextCancel(t *testing.T) {
	fd, c := newImageClient(t)
	release := make(chan struct{})
	t.Cleanup(func() { close(release) })
	execStartServer(t, fd, func(conn net.Conn, rw *bufio.ReadWriter) {
		rw.Write(frame(1, "partial"))
		rw.Flush()
		<-release
	})
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(100 * time.Millisecond)
		cancel()
	}()
	start := time.Now()
	_, _, err := c.ExecStart(ctx, "e")
	assert.ErrorIs(t, err, context.Canceled, "cancelling the context must surface as context.Canceled, not a closed-connection error")
	assert.Less(t, time.Since(start), 2*time.Second, "cancellation must unblock the read promptly")
}

func TestExecInspect(t *testing.T) {
	fd, c := newImageClient(t)
	fd.Handle("GET", "/exec/{id}/json", func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, `{"ID":"e","Running":false,"ExitCode":3,"ContainerID":"abc"}`)
	})
	ins, err := c.ExecInspect(context.Background(), "e")
	require.NoError(t, err, "exec inspect against the fake daemon")
	assert.Equal(t, ExecInspect{ID: "e", Running: false, ExitCode: 3, ContainerID: "abc"}, ins, "all inspect fields decode")
}

func TestExecCombinesAndWaitsForExit(t *testing.T) {
	fd, c := newImageClient(t)
	fd.Handle("POST", "/containers/{id}/exec", func(w http.ResponseWriter, r *http.Request) {
		fakedaemon.JSON(w, 201, execCreateResponse{ID: "e"})
	})
	execStartServer(t, fd, func(conn net.Conn, rw *bufio.ReadWriter) {
		rw.Write(frame(1, "1\n"))
		rw.Write(frame(2, "note\n"))
		rw.Flush()
	})
	inspects := 0
	fd.Handle("GET", "/exec/{id}/json", func(w http.ResponseWriter, r *http.Request) {
		inspects++
		fakedaemon.JSON(w, 200, ExecInspect{ID: "e", Running: inspects < 3, ExitCode: 1}) // running twice, then finished
	})
	res, err := c.Exec(context.Background(), "abc", "mongosh", "--quiet", "--eval", "1")
	require.NoError(t, err, "Exec against the fake daemon")
	assert.Equal(t, ExecResult{Stdout: "1\n", Stderr: "note\n", ExitCode: 1}, res, "Exec returns both streams and the exit code from inspect")
	assert.Equal(t, 3, inspects, "Exec must poll inspect until Running is false")
	var create execCreateRequest
	require.NoError(t, decodeBodyBytes(fd.Requests()[1].Body, &create), "the exec create body decodes")
	assert.Equal(t, []string{"mongosh", "--quiet", "--eval", "1"}, create.Cmd, "Exec passes the command through unchanged")

	_, err = c.Exec(context.Background(), "abc")
	assert.Same(t, ErrNoCommand, err, "Exec without a command returns the predeclared ErrNoCommand")
	assert.Contains(t, err.Error(), `Exec(ctx, containerID, "mongosh"`, "the error shows how to call it")
}

func TestDemuxEmptyStream(t *testing.T) {
	var out, errOut bytes.Buffer
	require.NoError(t, demux(bytes.NewReader(nil), &out, &errOut), "an empty stream is a clean EOF")
	assert.Empty(t, out.String(), "nothing on stdout")
	assert.Empty(t, errOut.String(), "nothing on stderr")
	assert.ErrorIs(t, errors.Unwrap(&StreamError{Err: io.EOF}), io.EOF, "StreamError unwraps its cause")
}
