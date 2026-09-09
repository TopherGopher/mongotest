package dockerapi

import (
	"bufio"
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/tophergopher/mongotest/internal/fakedaemon"
)

// frame builds one multiplexed stream frame.
func frame(stream byte, payload string) []byte {
	hdr := make([]byte, 8)
	hdr[0] = stream
	binary.BigEndian.PutUint32(hdr[4:], uint32(len(payload)))
	return append(hdr, payload...)
}

// hijackHandler upgrades the connection and hands the raw conn to fn.
func hijackHandler(t *testing.T, fn func(conn net.Conn, rw *bufio.ReadWriter)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Upgrade") != "tcp" || r.Header.Get("Connection") != "Upgrade" {
			fakedaemon.Error(w, 400, "missing upgrade headers")
			return
		}
		hj, ok := w.(http.Hijacker)
		if !ok {
			t.Error("response writer cannot hijack")
			return
		}
		conn, rw, err := hj.Hijack()
		if err != nil {
			t.Error(err)
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
		fakedaemon.JSON(w, 201, map[string]string{"Id": "exec-1"})
	})
	id, err := c.ExecCreate(context.Background(), "abc", ExecConfig{Cmd: []string{"mongosh", "--quiet", "--eval", "1"}, Env: []string{"A=1"}, WorkingDir: "/tmp"})
	if err != nil || id != "exec-1" {
		t.Fatalf("id=%q err=%v", id, err)
	}
	r := fd.Requests()[1]
	if r.Path != "/containers/abc/exec" {
		t.Fatalf("path %s", r.Path)
	}
	var got map[string]any
	_ = json.Unmarshal(r.Body, &got)
	if got["AttachStdout"] != true || got["AttachStderr"] != true || got["Tty"] != nil && got["Tty"] != false {
		t.Fatalf("attach flags: %v", got)
	}
	if cmd, _ := got["Cmd"].([]any); len(cmd) != 4 || cmd[0] != "mongosh" {
		t.Fatalf("cmd: %v", got["Cmd"])
	}
	if got["WorkingDir"] != "/tmp" {
		t.Fatalf("workingdir: %v", got["WorkingDir"])
	}
}

func execStartServer(t *testing.T, fd *fakedaemon.Server, fn func(conn net.Conn, rw *bufio.ReadWriter)) {
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
			if err != nil {
				t.Fatal(err)
			}
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
			if err != nil {
				t.Fatal(err)
			}
			if string(stdout) != "hello world\n" || string(stderr) != "warn" {
				t.Fatalf("stdout=%q stderr=%q", stdout, stderr)
			}
			r := fd.Requests()[1]
			if r.RawPath != "/v1.44/exec/exec-1/start" || strings.TrimSpace(string(r.Body)) != `{"Detach":false,"Tty":false}` {
				t.Fatalf("request %s body %s", r.RawPath, r.Body)
			}
		})
	}
}

func TestExecStartLargeFrame(t *testing.T) {
	fd, c := newImageClient(t)
	big := strings.Repeat("x", 300*1024)
	execStartServer(t, fd, func(conn net.Conn, rw *bufio.ReadWriter) {
		rw.Write(frame(1, big))
		rw.Flush()
	})
	stdout, _, err := c.ExecStart(context.Background(), "e")
	if err != nil || len(stdout) != len(big) {
		t.Fatalf("len=%d err=%v", len(stdout), err)
	}
}

func TestExecStartUnknownStreamType(t *testing.T) {
	fd, c := newImageClient(t)
	execStartServer(t, fd, func(conn net.Conn, rw *bufio.ReadWriter) {
		rw.Write(frame(7, "??"))
		rw.Flush()
	})
	_, _, err := c.ExecStart(context.Background(), "e")
	if err == nil || !strings.Contains(err.Error(), "stream type 7") {
		t.Fatalf("err = %v", err)
	}
}

func TestExecStartTruncatedFrame(t *testing.T) {
	fd, c := newImageClient(t)
	execStartServer(t, fd, func(conn net.Conn, rw *bufio.ReadWriter) {
		f := frame(1, "hello")
		rw.Write(f[:10]) // header plus two payload bytes, then EOF
		rw.Flush()
	})
	_, _, err := c.ExecStart(context.Background(), "e")
	if err == nil || !errors.Is(err, io.ErrUnexpectedEOF) {
		t.Fatalf("err = %v", err)
	}
}

func TestExecStartNonUpgradeResponse(t *testing.T) {
	fd, c := newImageClient(t)
	fd.Handle("POST", "/exec/{id}/start", func(w http.ResponseWriter, r *http.Request) {
		fakedaemon.Error(w, 404, "No such exec instance: e")
	})
	_, _, err := c.ExecStart(context.Background(), "e")
	if !IsNotFound(err) || !strings.Contains(err.Error(), "No such exec instance") {
		t.Fatalf("err = %v", err)
	}
}

func TestExecStart200RawStreamIsAccepted(t *testing.T) {
	// Older daemons answer 200 with the stream as the body instead of 101.
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
	if err != nil || string(stdout) != "ok" {
		t.Fatalf("stdout=%q err=%v", stdout, err)
	}
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
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v", err)
	}
	if time.Since(start) > 2*time.Second {
		t.Fatal("cancellation was not prompt")
	}
}

func TestExecInspect(t *testing.T) {
	fd, c := newImageClient(t)
	fd.Handle("GET", "/exec/{id}/json", func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, `{"ID":"e","Running":false,"ExitCode":3,"ContainerID":"abc"}`)
	})
	ins, err := c.ExecInspect(context.Background(), "e")
	if err != nil || ins.Running || ins.ExitCode != 3 || ins.ContainerID != "abc" {
		t.Fatalf("ins=%+v err=%v", ins, err)
	}
}

func TestExecCombinesAndWaitsForExit(t *testing.T) {
	fd, c := newImageClient(t)
	fd.Handle("POST", "/containers/{id}/exec", func(w http.ResponseWriter, r *http.Request) {
		fakedaemon.JSON(w, 201, map[string]string{"Id": "e"})
	})
	execStartServer(t, fd, func(conn net.Conn, rw *bufio.ReadWriter) {
		rw.Write(frame(1, "1\n"))
		rw.Write(frame(2, "note\n"))
		rw.Flush()
	})
	inspects := 0
	fd.Handle("GET", "/exec/{id}/json", func(w http.ResponseWriter, r *http.Request) {
		inspects++
		running := inspects < 3 // report running twice, then finished
		fakedaemon.JSON(w, 200, map[string]any{"ID": "e", "Running": running, "ExitCode": 1})
	})
	res, err := c.Exec(context.Background(), "abc", "mongosh", "--quiet", "--eval", "1")
	if err != nil {
		t.Fatal(err)
	}
	if res.Stdout != "1\n" || res.Stderr != "note\n" || res.ExitCode != 1 {
		t.Fatalf("res=%+v", res)
	}
	if inspects != 3 {
		t.Fatalf("inspect polled %d times, want 3", inspects)
	}
	var create map[string]any
	_ = json.Unmarshal(fd.Requests()[1].Body, &create)
	if cmd, _ := create["Cmd"].([]any); len(cmd) != 4 || cmd[3] != "1" {
		t.Fatalf("exec create cmd %v", create["Cmd"])
	}
}

func TestDemuxEmptyStream(t *testing.T) {
	stdout, stderr, err := demux(bytes.NewReader(nil))
	if err != nil || len(stdout) != 0 || len(stderr) != 0 {
		t.Fatalf("stdout=%q stderr=%q err=%v", stdout, stderr, err)
	}
}

func TestExecStartDaemonErrorFrame(t *testing.T) {
	fd, c := newImageClient(t)
	execStartServer(t, fd, func(conn net.Conn, rw *bufio.ReadWriter) {
		rw.Write(frame(1, "partial"))
		rw.Write(frame(3, "container abc is not running"))
		rw.Flush()
	})
	stdout, _, err := c.ExecStart(context.Background(), "e")
	if err == nil || !strings.Contains(err.Error(), "container abc is not running") {
		t.Fatalf("err = %v", err)
	}
	if string(stdout) != "partial" {
		t.Fatalf("output before the error frame must be kept, got %q", stdout)
	}
}
