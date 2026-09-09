package dockerapi

import (
	"archive/tar"
	"bufio"
	"bytes"
	"errors"
	"io"
	"io/fs"
	"net"
	"net/http"
	"net/url"
	"path"
	"strconv"
	"strings"
	"testing"
)

// The parsers and validators in this package stand between caller input and
// a URL path or a request body sent to the daemon, so the properties fuzzed
// here are safety properties: never panic on any input, and never accept a
// value that could change the shape of a request.

func FuzzParseHost(f *testing.F) {
	for _, seed := range []string{
		"unix:///var/run/docker.sock", "tcp://127.0.0.1:2375", "tcp://docker:2376",
		"http://10.0.0.5:2375", "https://build:2376", "npipe:////./pipe/docker_engine",
		"unix://", "", "no-scheme", "tcp://", "tcp://[::1]:2375", "unix:///a b/c.sock",
		"tcp://host:99999", "://", "unix:///x?y=z", "TCP://HOST:1",
		// Regressions found by this target: an address with no host and no
		// port, a host with stray colons, and one with a stray bracket.
		"http://:", "tcp://:0:", "tcp://a]0", "tcp://:2375", "tcp://host:",
	} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, host string) {
		ep, err := parseHost(host)
		if err != nil {
			// Every rejection is an invalid argument, so callers can branch
			// on one sentinel.
			if !errors.Is(err, ErrInvalidArgument) {
				t.Fatalf("parseHost(%q) failed with %v, which does not match ErrInvalidArgument", host, err)
			}
			return
		}
		switch ep.scheme {
		case "unix":
			if ep.addr == "" {
				t.Fatalf("parseHost(%q) accepted a unix endpoint with no socket path", host)
			}
			if ep.tls {
				t.Fatalf("parseHost(%q) marked a unix socket as TLS", host)
			}
		case "tcp":
			// An accepted tcp endpoint must be dialable, which means
			// SplitHostPort has to succeed on it.
			if _, port, err := net.SplitHostPort(ep.addr); err != nil {
				t.Fatalf("parseHost(%q) accepted addr %q that is not host:port: %v", host, ep.addr, err)
			} else if port == "" {
				t.Fatalf("parseHost(%q) accepted addr %q with an empty port", host, ep.addr)
			}
		default:
			t.Fatalf("parseHost(%q) returned unknown scheme %q", host, ep.scheme)
		}
		// The base URL must always be a parseable absolute URL.
		u, err := url.Parse(ep.baseURL(ep.tls))
		if err != nil || u.Host == "" {
			t.Fatalf("parseHost(%q) produced base URL %q: %v", host, ep.baseURL(ep.tls), err)
		}
	})
}

func FuzzParseImageRef(f *testing.F) {
	for _, seed := range []string{
		"mongo", "mongo:8", "docker.io/library/mongo:8", "localhost:5000/team/mongo:8.0",
		"mongo@sha256:" + strings.Repeat("a", 64), "mongo:8@sha256:" + strings.Repeat("0", 64),
		"[::1]:5000/mongo", "Mongo:8", "", "mongo:", ":8", "mongo//x", "mongo:8/json",
		"my__image.name-x", "a/b/c/d:tag", "mongo@notadigest", strings.Repeat("a", 300),
	} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, ref string) {
		r, err := parseImageRef(ref)
		if err != nil {
			if !errors.Is(err, ErrInvalidArgument) {
				t.Fatalf("parseImageRef(%q) failed with %v, which does not match ErrInvalidArgument", ref, err)
			}
			return
		}
		if r.name == "" {
			t.Fatalf("parseImageRef(%q) accepted an empty repository name", ref)
		}
		if r.tag == "" && r.digest == "" {
			t.Fatalf("parseImageRef(%q) returned neither a tag nor a digest", ref)
		}
		if len(r.name) > refNameMaxLength {
			t.Fatalf("parseImageRef(%q) accepted a %d character name", ref, len(r.name))
		}
		// The name goes into a URL path and the tag into a query value, so
		// neither may carry characters that would restructure the request.
		for _, bad := range []string{" ", "\t", "\n", "\r", "?", "#", "%"} {
			if strings.Contains(r.name, bad) || strings.Contains(r.tag, bad) || strings.Contains(r.digest, bad) {
				t.Fatalf("parseImageRef(%q) = %+v contains %q", ref, r, bad)
			}
		}
		// Parsing is stable: the canonical form of an accepted reference
		// parses back to the same parts.
		canonical := r.name
		if r.tag != "" {
			canonical += ":" + r.tag
		}
		if r.digest != "" {
			canonical += "@" + r.digest
		}
		again, err := parseImageRef(canonical)
		if err != nil {
			t.Fatalf("parseImageRef(%q) produced %q, which no longer parses: %v", ref, canonical, err)
		}
		if again != r {
			t.Fatalf("parseImageRef is not stable: %q -> %+v -> %q -> %+v", ref, r, canonical, again)
		}
	})
}

func FuzzCheckID(f *testing.F) {
	for _, seed := range []string{
		"abc123", " abc ", "/mongotest-1", "de686f1e8de9", "", "a/b", "../x",
		"abc?force=1", "abc#frag", "a b", "a\nb", "-abc", ".abc", "%2e%2e",
		strings.Repeat("a", 200), "ID_with.dots-and_dashes",
	} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, id string) {
		got, err := checkID("container", id)
		if err != nil {
			if !errors.Is(err, ErrInvalidArgument) {
				t.Fatalf("checkID(%q) failed with %v, which does not match ErrInvalidArgument", id, err)
			}
			return
		}
		if got == "" {
			t.Fatalf("checkID(%q) accepted an empty id", id)
		}
		// The accepted id is concatenated straight into a URL path, so it
		// must survive path escaping unchanged: anything else would let a
		// caller reach a different endpoint.
		if escaped := url.PathEscape(got); escaped != got {
			t.Fatalf("checkID(%q) = %q, which escapes to %q and would change the request path", id, got, escaped)
		}
		for _, bad := range []string{"/", "?", "#", "%", " "} {
			if strings.Contains(got, bad) {
				t.Fatalf("checkID(%q) = %q contains %q", id, got, bad)
			}
		}
		// Dots are legal in daemon ids and names ("0.0.." is a valid
		// container name), so what matters is not the characters but that
		// the id stays one path segment: cleaning the URL must not move it.
		requested := "/containers/" + got + "/json"
		if cleaned := path.Clean(requested); cleaned != requested {
			t.Fatalf("checkID(%q) = %q makes %q clean to %q, so it would reach a different endpoint", id, got, requested, cleaned)
		}
		// Checking an accepted id again is a no-op.
		again, err := checkID("container", got)
		if err != nil || again != got {
			t.Fatalf("checkID is not stable: %q -> %q -> %q, %v", id, got, again, err)
		}
	})
}

func FuzzCheckContainerName(f *testing.F) {
	for _, seed := range []string{
		"mongotest-1", "/mongotest-1", "", " ", "x", "ab", "bad name!", "a/b",
		"UPPER_case.1", strings.Repeat("n", 300),
	} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, name string) {
		got, err := checkContainerName(name)
		if err != nil {
			if !errors.Is(err, ErrInvalidArgument) {
				t.Fatalf("checkContainerName(%q) failed with %v, which does not match ErrInvalidArgument", name, err)
			}
			return
		}
		if got == "" {
			return // an empty name is allowed; the daemon generates one
		}
		// The name travels as a query value; it must not need escaping.
		if url.QueryEscape(got) != got {
			t.Fatalf("checkContainerName(%q) = %q, which is not safe as a query value", name, got)
		}
		if len(got) < 2 {
			t.Fatalf("checkContainerName(%q) = %q, shorter than the daemon's two character minimum", name, got)
		}
	})
}

func FuzzCheckPorts(f *testing.F) {
	for _, seed := range []string{"27017/tcp", "27017", "0/tcp", "70000/tcp", "27017/icmp", "", "/tcp", "-1"} {
		f.Add(seed, "34819")
	}
	f.Add("27017/tcp", "40000-40010")
	f.Add("27017/tcp", "")
	f.Fuzz(func(t *testing.T, key, hostPort string) {
		if err := checkPortKey("ExposedPorts", key); err != nil {
			if !errors.Is(err, ErrInvalidArgument) {
				t.Fatalf("checkPortKey(%q) failed with %v, which does not match ErrInvalidArgument", key, err)
			}
		} else {
			port, proto, _ := strings.Cut(key, "/")
			n, convErr := strconv.Atoi(port)
			if convErr != nil || n < 1 || n > 65535 {
				t.Fatalf("checkPortKey accepted %q with port %q", key, port)
			}
			switch strings.ToLower(proto) {
			case "", "tcp", "udp", "sctp":
			default:
				t.Fatalf("checkPortKey accepted %q with protocol %q", key, proto)
			}
		}

		if err := checkHostPort("PortBindings", hostPort); err != nil {
			if !errors.Is(err, ErrInvalidArgument) {
				t.Fatalf("checkHostPort(%q) failed with %v, which does not match ErrInvalidArgument", hostPort, err)
			}
			return
		}
		if hostPort == "" {
			return // the daemon assigns one
		}
		// Anything accepted must be a port or an ordered range, since it
		// goes into the create body verbatim.
		lo, hi, isRange := strings.Cut(hostPort, "-")
		l, err := strconv.Atoi(lo)
		if err != nil || l < 0 || l > 65535 {
			t.Fatalf("checkHostPort accepted %q with low bound %q", hostPort, lo)
		}
		if isRange {
			h, err := strconv.Atoi(hi)
			if err != nil || h < l || h > 65535 {
				t.Fatalf("checkHostPort accepted %q with high bound %q", hostPort, hi)
			}
		}
	})
}

func FuzzCompareVersions(f *testing.F) {
	for _, pair := range [][2]string{
		{"1.44", "1.44"}, {"1.44", "1.9"}, {"1.41", "1.44"}, {"2.0", "1.99"},
		{"1.44", ""}, {"", ""}, {"x.y", "1.0"}, {"1", "1.0"}, {"01.044", "1.44"},
	} {
		f.Add(pair[0], pair[1])
	}
	f.Fuzz(func(t *testing.T, a, b string) {
		ab := compareVersions(a, b)
		ba := compareVersions(b, a)
		if ab != -ba {
			t.Fatalf("compareVersions(%q,%q)=%d but compareVersions(%q,%q)=%d; the ordering is not antisymmetric", a, b, ab, b, a, ba)
		}
		if got := compareVersions(a, a); got != 0 {
			t.Fatalf("compareVersions(%q,%q)=%d, a version must equal itself", a, a, got)
		}
		if ab < -1 || ab > 1 {
			t.Fatalf("compareVersions(%q,%q)=%d, outside -1..1", a, b, ab)
		}
	})
}

func FuzzDemux(f *testing.F) {
	// Well-formed frames, a truncated frame, an unknown stream type and a
	// daemon error frame.
	f.Add(append(fuzzFrame(1, "hello"), fuzzFrame(2, "warn")...))
	f.Add(fuzzFrame(1, "hello")[:10])
	f.Add(fuzzFrame(7, "??"))
	f.Add(fuzzFrame(3, "container is not running"))
	f.Add([]byte{})
	f.Add([]byte{1, 0, 0, 0, 255, 255, 255, 255}) // a header claiming 4 GiB
	f.Fuzz(func(t *testing.T, stream []byte) {
		var out, errOut bytes.Buffer
		err := demux(bytes.NewReader(stream), &out, &errOut)
		if err != nil && !errors.Is(err, ErrStream) {
			t.Fatalf("demux returned %v, which does not match ErrStream", err)
		}
		// A frame header must never make the decoder produce or buffer more
		// than the bytes it was given.
		if total := out.Len() + errOut.Len(); total > len(stream) {
			t.Fatalf("demux wrote %d bytes from a %d byte stream", total, len(stream))
		}
	})
}

// fuzzFrame builds one multiplexed stream frame for the seed corpus.
func fuzzFrame(stream byte, payload string) []byte {
	hdr := make([]byte, 8)
	hdr[0] = stream
	hdr[4] = byte(len(payload) >> 24)
	hdr[5] = byte(len(payload) >> 16)
	hdr[6] = byte(len(payload) >> 8)
	hdr[7] = byte(len(payload))
	return append(hdr, payload...)
}

func FuzzDrainPullStream(f *testing.F) {
	f.Add(`{"status":"Downloading"}` + "\n")
	f.Add(`{"error":"manifest unknown"}` + "\n")
	f.Add(`{"errorDetail":{"message":"boom"},"error":"boom"}` + "\n")
	f.Add("not json\n{\"status\":\"ok\"}\n")
	f.Add("")
	f.Add(strings.Repeat("{", 1000))
	f.Fuzz(func(t *testing.T, stream string) {
		err := drainPullStream(bufio.NewScanner(strings.NewReader(stream)), "mongo:8")
		if err != nil && !errors.Is(err, ErrPull) {
			t.Fatalf("drainPullStream returned %v, which does not match ErrPull", err)
		}
	})
}

func FuzzValidateAndWriteTar(f *testing.F) {
	for _, seed := range []struct {
		name string
		mode uint32
		body string
	}{
		{"ca.pem", 0o644, "ca"},
		{"mongo-tls/server.pem", 0o600, "cert"},
		{"../escape", 0o644, "x"},
		{"/absolute", 0o644, "x"},
		{"", 0o644, ""},
		{"a/../../b", 0o644, "x"},
		{"dir/", 0o755, ""},
		{"ok", uint32(fs.ModeDir | 0o755), "x"},
		// Regression found by this target: a name archive/tar cannot encode
		// used to pass validation and fail later, mid-upload.
		{"\x00", 0o644, "0"},
		{"ca\npem", 0o644, "x"},
	} {
		f.Add(seed.name, seed.mode, seed.body)
	}
	f.Fuzz(func(t *testing.T, name string, mode uint32, body string) {
		files := []File{{Name: name, Mode: fs.FileMode(mode), Content: []byte(body)}}
		if err := validateFiles(files); err != nil {
			if !errors.Is(err, ErrInvalidArgument) {
				t.Fatalf("validateFiles(%q) failed with %v, which does not match ErrInvalidArgument", name, err)
			}
			return
		}
		var buf bytes.Buffer
		if err := writeTar(&buf, files); err != nil {
			t.Fatalf("writeTar rejected the file %q that validateFiles accepted: %v", name, err)
		}
		// Whatever the caller passed, the archive must read back cleanly and
		// must not be able to place a file outside the destination
		// directory once the daemon extracts it.
		tr := tar.NewReader(&buf)
		for {
			hdr, err := tr.Next()
			if errors.Is(err, io.EOF) {
				break
			}
			if err != nil {
				t.Fatalf("the archive built from %q is not readable: %v", name, err)
			}
			if path.IsAbs(hdr.Name) {
				t.Fatalf("archive entry %q from input %q is absolute", hdr.Name, name)
			}
			joined := path.Join("/dest", hdr.Name)
			if !strings.HasPrefix(joined, "/dest/") {
				t.Fatalf("archive entry %q from input %q escapes the destination as %q", hdr.Name, name, joined)
			}
			if _, err := io.Copy(io.Discard, tr); err != nil {
				t.Fatalf("reading entry %q back: %v", hdr.Name, err)
			}
		}
	})
}

func FuzzNewStatusError(f *testing.F) {
	f.Add(404, `{"message":"No such container: x"}`)
	f.Add(500, "<html>oops</html>")
	f.Add(409, "")
	f.Add(400, `{"message":`+strconv.Quote(strings.Repeat("m", 5000))+`}`)
	f.Add(200, "{}")
	// Regression found by this target: a status code outside the registered
	// set has no status text, which used to leave the message empty.
	f.Add(368, "")
	f.Fuzz(func(t *testing.T, status int, body string) {
		if status < 100 || status > 599 {
			t.Skip("not an HTTP status code")
		}
		resp := &http.Response{StatusCode: status, Body: io.NopCloser(strings.NewReader(body))}
		se := newStatusError(resp, http.MethodGet, "/containers/x/json")
		if se.Message == "" {
			t.Fatalf("newStatusError(%d, %q) produced an empty message", status, body)
		}
		// Daemon messages are attacker-influenced in the sense that they can
		// be arbitrarily long; the error must stay printable and bounded.
		if len(se.Message) > 200 {
			t.Fatalf("newStatusError(%d) message is %d bytes, above the 200 byte cap", status, len(se.Message))
		}
		if se.Error() == "" {
			t.Fatalf("newStatusError(%d) formatted to an empty string", status)
		}
		if status == http.StatusNotFound && !IsNotFound(se) {
			t.Fatalf("a 404 did not match ErrNotFound")
		}
	})
}
