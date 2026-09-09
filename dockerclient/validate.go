package dockerclient

import (
	"io/fs"
	"net"
	"path"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
)

// Input validation mirrors what the docker CLI and the daemon enforce, so a
// bad value fails fast with an InvalidArgumentError that says what to fix,
// instead of producing a confusing 404 or 400 from a mangled URL. It lives
// here rather than in one client so that every implementation rejects the
// same values with the same message, which the conformance suite asserts.

var (
	// idPattern accepts container ids, container names and exec ids. A
	// leading slash (as returned by inspect for names) is tolerated by
	// checkID and stripped.
	idPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]*$`)
	// containerNamePattern is the daemon's rule for user-chosen names.
	containerNamePattern = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_.-]+$`)
	// imageIDPattern matches a content digest or a (possibly truncated) hex
	// image id, both accepted by image inspect.
	imageIDPattern = regexp.MustCompile(`^(sha256:)?[0-9a-fA-F]{12,64}$`)
)

// Image reference grammar, transcribed from github.com/distribution/reference.
const (
	refAlphanumeric  = `[a-z0-9]+`
	refSeparator     = `(?:[._]|__|[-]+)`
	refPathComponent = refAlphanumeric + `(?:` + refSeparator + refAlphanumeric + `)*`
	refDomainPart    = `(?:[a-zA-Z0-9]|[a-zA-Z0-9][a-zA-Z0-9-]*[a-zA-Z0-9])`
	refIPv6          = `\[(?:[a-fA-F0-9:]+)\]`
	refDomainName    = refDomainPart + `(?:\.` + refDomainPart + `)*`
	refHost          = `(?:` + refDomainName + `|` + refIPv6 + `)`
	refDomainAndPort = refHost + `(?::[0-9]+)?`
	refTag           = `[\w][\w.-]{0,127}`
	refDigest        = `[A-Za-z][A-Za-z0-9]*(?:[-_+.][A-Za-z][A-Za-z0-9]*)*:[0-9A-Fa-f]{32,}`
	refRemoteName    = refPathComponent + `(?:/` + refPathComponent + `)*`
	refName          = `(?:` + refDomainAndPort + `/)?` + refRemoteName
	RefNameMaxLength = 255
)

var (
	referencePattern = regexp.MustCompile(`^(` + refName + `)(?::(` + refTag + `))?(?:@(` + refDigest + `))?$`)
	refNamePattern   = regexp.MustCompile(`^` + refName + `$`)
)

const refFix = `use the form [registry[:port]/]repository[:tag|@digest] with a lowercase repository, for example "mongo:8" or "localhost:5000/team/mongo@sha256:..."`

// ImageRef is a parsed image reference.
type ImageRef struct {
	// Name is the repository, including any registry host:
	// "mongo", "localhost:5000/team/mongo".
	Name string
	// Tag is the tag, or "latest" when neither tag nor digest was given.
	Tag string
	// Digest is the content digest ("sha256:..."), or "" when none was given.
	Digest string
}

// parseImageRef validates and splits an image reference such as "mongo:8"
// or "localhost:5000/mongo@sha256:...". The repository part must be
// lowercase; only a registry host may contain uppercase letters.
func ParseImageRef(ref string) (ImageRef, error) {
	if strings.TrimSpace(ref) == "" {
		return ImageRef{}, InvalidArgument("image reference", "", "the value is empty", refFix)
	}
	m := referencePattern.FindStringSubmatch(ref)
	if m == nil {
		lowered := strings.ToLower(strings.SplitN(strings.SplitN(ref, "@", 2)[0], ":", 2)[0])
		if refNamePattern.MatchString(lowered) {
			return ImageRef{}, InvalidArgument("image reference", ref, "repository names must be lowercase", refFix)
		}
		return ImageRef{}, InvalidArgument("image reference", ref, "it does not match the docker reference grammar", refFix)
	}
	r := ImageRef{Name: m[1], Tag: m[2], Digest: m[3]}
	if len(r.Name) > RefNameMaxLength {
		return ImageRef{}, InvalidArgument("image reference", ref, "the repository name is longer than 255 characters", "shorten the repository path")
	}
	if r.Tag == "" && r.Digest == "" {
		r.Tag = "latest"
	}
	return r, nil
}

// checkID trims and validates an object id or name for use in a URL path.
// A leading "/" (inspect reports names that way) is removed.
func CheckID(kind, id string) (string, error) {
	trimmed := strings.TrimPrefix(strings.TrimSpace(id), "/")
	if trimmed == "" {
		return "", InvalidArgument(kind+" id", "", "the value is empty", "pass the id returned by ContainerCreate or ExecCreate, or the container name")
	}
	if !idPattern.MatchString(trimmed) {
		return "", InvalidArgument(kind+" id", id, "only letters, digits, '_', '.' and '-' are allowed", "pass the id returned by ContainerCreate or ExecCreate, or the container name")
	}
	return trimmed, nil
}

// checkContainerName validates a user-chosen container name. Empty is
// allowed (the daemon generates one).
func CheckContainerName(name string) (string, error) {
	trimmed := strings.TrimPrefix(strings.TrimSpace(name), "/")
	if trimmed == "" {
		return "", nil
	}
	if !containerNamePattern.MatchString(trimmed) {
		return "", InvalidArgument("container name", name, "names need at least two characters from [a-zA-Z0-9][a-zA-Z0-9_.-]", `choose a name like "mongotest-1a2b" or pass "" to let the daemon pick one`)
	}
	return trimmed, nil
}

// checkImageRefOrID accepts an image reference or an image id for inspect.
func CheckImageRefOrID(ref string) (string, error) {
	trimmed := strings.TrimSpace(ref)
	if imageIDPattern.MatchString(trimmed) {
		return trimmed, nil
	}
	if _, err := ParseImageRef(trimmed); err != nil {
		return "", err
	}
	return trimmed, nil
}

// checkPortKey validates a "port[/proto]" key as used in ExposedPorts and
// PortBindings.
func CheckPortKey(field, key string) error {
	port, proto, _ := strings.Cut(key, "/")
	n, err := strconv.Atoi(port)
	if err != nil || n < 1 || n > 65535 {
		return InvalidArgument(field, key, "the port must be a number from 1 to 65535", `use keys like "27017/tcp"`)
	}
	switch strings.ToLower(proto) {
	case "", "tcp", "udp", "sctp":
		return nil
	}
	return InvalidArgument(field, key, "the protocol must be tcp, udp or sctp", `use keys like "27017/tcp"`)
}

// checkHostPort validates a PortBinding.HostPort: empty (daemon assigns),
// a single port, or a "low-high" range.
func CheckHostPort(field, hp string) error {
	if hp == "" {
		return nil
	}
	lo, hi, isRange := strings.Cut(hp, "-")
	parse := func(s string) (int, bool) {
		n, err := strconv.Atoi(s)
		return n, err == nil && n >= 0 && n <= 65535
	}
	l, ok := parse(lo)
	if !ok {
		return InvalidArgument(field, hp, "the host port must be a number from 0 to 65535", `use "" to let the daemon choose, "34819" to pin a port, or "40000-40010" for a range`)
	}
	if isRange {
		h, ok := parse(hi)
		if !ok || h < l {
			return InvalidArgument(field, hp, "the host port range must be low-high with low <= high", `use "40000-40010"`)
		}
	}
	return nil
}

// Validate checks a ContainerConfig before it is sent to a daemon.
func (cfg ContainerConfig) Validate() error {
	if strings.TrimSpace(cfg.Image) == "" {
		return InvalidArgument("ContainerConfig.Image", "", "the image is required", `set Image to a reference such as "mongo:8"`)
	}
	if _, err := CheckImageRefOrID(cfg.Image); err != nil {
		return err
	}
	for i, arg := range cfg.Cmd {
		if arg == "" {
			return InvalidArgument("ContainerConfig.Cmd["+strconv.Itoa(i)+"]", "", "an empty argument was given", "remove the empty element; the image entrypoint prepends mongod, so pass flags only")
		}
	}
	for k := range cfg.Labels {
		if strings.TrimSpace(k) == "" {
			return InvalidArgument("ContainerConfig.Labels", "", "a label has an empty key", `use keys like "mongotest"`)
		}
	}
	for k := range cfg.ExposedPorts {
		if err := CheckPortKey("ContainerConfig.ExposedPorts key", k); err != nil {
			return err
		}
	}
	if cfg.HostConfig != nil {
		for k, bindings := range cfg.HostConfig.PortBindings {
			if err := CheckPortKey("HostConfig.PortBindings key", k); err != nil {
				return err
			}
			field := "HostConfig.PortBindings[" + k + "]"
			for _, b := range bindings {
				if b.HostIP != "" && net.ParseIP(b.HostIP) == nil {
					return InvalidArgument(field+".HostIP", b.HostIP, "it is not an IP address", `use "127.0.0.1" to publish on loopback only, or "" for all interfaces`)
				}
				if err := CheckHostPort(field+".HostPort", b.HostPort); err != nil {
					return err
				}
			}
		}
	}
	return nil
}

// Validate checks an ExecConfig against the API version the client
// negotiated, since Env and WorkingDir each need a minimum.
func (cfg ExecConfig) Validate(apiVersion string) error {
	if len(cfg.Cmd) == 0 || strings.TrimSpace(cfg.Cmd[0]) == "" {
		return ErrNoCommand
	}
	for _, e := range cfg.Env {
		if !strings.Contains(e, "=") || strings.HasPrefix(e, "=") {
			return InvalidArgument("ExecConfig.Env entry", e, "it is not in KEY=value form", `use entries like "MONGO_INITDB_DATABASE=test"`)
		}
	}
	if len(cfg.Env) > 0 && compareAPIVersions(apiVersion, "1.25") < 0 {
		return &APIVersionError{Feature: "ExecConfig.Env", Required: "1.25", Negotiated: apiVersion}
	}
	if cfg.WorkingDir != "" {
		if !strings.HasPrefix(cfg.WorkingDir, "/") {
			return InvalidArgument("ExecConfig.WorkingDir", cfg.WorkingDir, "it must be an absolute path inside the container", `use "/tmp" or another absolute path`)
		}
		if compareAPIVersions(apiVersion, "1.35") < 0 {
			return &APIVersionError{Feature: "ExecConfig.WorkingDir", Required: "1.35", Negotiated: apiVersion}
		}
	}
	return nil
}

// ValidateFiles checks file names and modes before any request is
// started, so a bad entry never leaves a half-written upload behind.
func ValidateFiles(files []File) error {
	if len(files) == 0 {
		return ErrNoFiles
	}
	for _, f := range files {
		name := path.Clean(f.Name)
		if name == "." || name == "" || strings.HasPrefix(name, "/") || strings.HasPrefix(name, "../") || name == ".." {
			return InvalidArgument("file name", f.Name, "it must be relative to the destination directory and must not contain '..'", `use names like "ca.pem" or "mongo-tls/server.pem"`)
		}
		// archive/tar cannot encode a path containing a control character,
		// and discovering that inside writeTar would fail the upload after
		// the request had already started.
		if strings.ContainsFunc(name, func(r rune) bool { return r < 0x20 || r == 0x7f }) {
			return InvalidArgument("file name", strconv.Quote(f.Name), "it contains a control character, which cannot be stored in a tar archive",
				`use a plain relative path such as "mongo-tls/server.pem"`)
		}
		if f.Mode&^fs.ModePerm != 0 {
			return InvalidArgument("file mode", f.Mode.String(), "only permission bits are allowed (no type, setuid, setgid or sticky bits)", "use a value like 0o644 or 0o600, or 0 for the default 0o644")
		}
	}
	return nil
}

// CheckDestDir validates the directory an archive is extracted into. The
// daemon interprets the path inside the container, so a relative path has no
// meaning and a traversal would place files outside the destination the
// caller named.
//
// Every implementation applies this, so a caller who tested against a double
// gets the same answer from a real daemon.
func CheckDestDir(destDir string) (string, error) {
	dest := filepath.ToSlash(destDir)
	if dest == "" {
		return "", InvalidArgument("destination directory", destDir,
			"no destination was given", `pass a path inside the container, for example "/etc/mongo-tls"`)
	}
	if !strings.HasPrefix(dest, "/") {
		return "", InvalidArgument("destination directory", destDir,
			"it must be an absolute path inside the container",
			`use a path like "/etc/mongo-tls" or "/tmp"`)
	}
	if dest == "/.." || strings.Contains(dest, "/../") || strings.HasSuffix(dest, "/..") {
		return "", InvalidArgument("destination directory", destDir,
			"it walks outside itself with ..",
			"give the destination directly, without .. segments")
	}
	return dest, nil
}
