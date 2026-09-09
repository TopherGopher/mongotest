package dockerclient_test

import (
	"errors"
	"net/url"
	"path"
	"strconv"
	"strings"
	"testing"

	"github.com/tophergopher/mongotest/dockerclient"
)

// These validators stand between caller input and a URL path or a request
// body sent to the daemon, and every implementation of Client applies them,
// so the properties fuzzed here are safety properties: never panic on any
// input, and never accept a value that could change the shape of a request.

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
		r, err := dockerclient.ParseImageRef(ref)
		if err != nil {
			if !errors.Is(err, dockerclient.ErrInvalidArgument) {
				t.Fatalf("dockerclient.ParseImageRef(%q) failed with %v, which does not match dockerclient.ErrInvalidArgument", ref, err)
			}
			return
		}
		if r.Name == "" {
			t.Fatalf("dockerclient.ParseImageRef(%q) accepted an empty repository name", ref)
		}
		if r.Tag == "" && r.Digest == "" {
			t.Fatalf("dockerclient.ParseImageRef(%q) returned neither a tag nor a digest", ref)
		}
		if len(r.Name) > dockerclient.RefNameMaxLength {
			t.Fatalf("dockerclient.ParseImageRef(%q) accepted a %d character name", ref, len(r.Name))
		}
		// The name goes into a URL path and the tag into a query value, so
		// neither may carry characters that would restructure the request.
		for _, bad := range []string{" ", "\t", "\n", "\r", "?", "#", "%"} {
			if strings.Contains(r.Name, bad) || strings.Contains(r.Tag, bad) || strings.Contains(r.Digest, bad) {
				t.Fatalf("dockerclient.ParseImageRef(%q) = %+v contains %q", ref, r, bad)
			}
		}
		// Parsing is stable: the canonical form of an accepted reference
		// parses back to the same parts.
		canonical := r.Name
		if r.Tag != "" {
			canonical += ":" + r.Tag
		}
		if r.Digest != "" {
			canonical += "@" + r.Digest
		}
		again, err := dockerclient.ParseImageRef(canonical)
		if err != nil {
			t.Fatalf("dockerclient.ParseImageRef(%q) produced %q, which no longer parses: %v", ref, canonical, err)
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
		got, err := dockerclient.CheckID("container", id)
		if err != nil {
			if !errors.Is(err, dockerclient.ErrInvalidArgument) {
				t.Fatalf("dockerclient.CheckID(%q) failed with %v, which does not match dockerclient.ErrInvalidArgument", id, err)
			}
			return
		}
		if got == "" {
			t.Fatalf("dockerclient.CheckID(%q) accepted an empty id", id)
		}
		// The accepted id is concatenated straight into a URL path, so it
		// must survive path escaping unchanged: anything else would let a
		// caller reach a different endpoint.
		if escaped := url.PathEscape(got); escaped != got {
			t.Fatalf("dockerclient.CheckID(%q) = %q, which escapes to %q and would change the request path", id, got, escaped)
		}
		for _, bad := range []string{"/", "?", "#", "%", " "} {
			if strings.Contains(got, bad) {
				t.Fatalf("dockerclient.CheckID(%q) = %q contains %q", id, got, bad)
			}
		}
		// Dots are legal in daemon ids and names ("0.0.." is a valid
		// container name), so what matters is not the characters but that
		// the id stays one path segment: cleaning the URL must not move it.
		requested := "/containers/" + got + "/json"
		if cleaned := path.Clean(requested); cleaned != requested {
			t.Fatalf("dockerclient.CheckID(%q) = %q makes %q clean to %q, so it would reach a different endpoint", id, got, requested, cleaned)
		}
		// Checking an accepted id again is a no-op.
		again, err := dockerclient.CheckID("container", got)
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
		got, err := dockerclient.CheckContainerName(name)
		if err != nil {
			if !errors.Is(err, dockerclient.ErrInvalidArgument) {
				t.Fatalf("dockerclient.CheckContainerName(%q) failed with %v, which does not match dockerclient.ErrInvalidArgument", name, err)
			}
			return
		}
		if got == "" {
			return // an empty name is allowed; the daemon generates one
		}
		// The name travels as a query value; it must not need escaping.
		if url.QueryEscape(got) != got {
			t.Fatalf("dockerclient.CheckContainerName(%q) = %q, which is not safe as a query value", name, got)
		}
		if len(got) < 2 {
			t.Fatalf("dockerclient.CheckContainerName(%q) = %q, shorter than the daemon's two character minimum", name, got)
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
		if err := dockerclient.CheckPortKey("ExposedPorts", key); err != nil {
			if !errors.Is(err, dockerclient.ErrInvalidArgument) {
				t.Fatalf("dockerclient.CheckPortKey(%q) failed with %v, which does not match dockerclient.ErrInvalidArgument", key, err)
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

		if err := dockerclient.CheckHostPort("PortBindings", hostPort); err != nil {
			if !errors.Is(err, dockerclient.ErrInvalidArgument) {
				t.Fatalf("dockerclient.CheckHostPort(%q) failed with %v, which does not match dockerclient.ErrInvalidArgument", hostPort, err)
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
