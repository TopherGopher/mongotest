package dockerapi

import (
	"net"
	"regexp"
	"strconv"
	"strings"
)

// Input validation mirrors what the docker CLI and the daemon enforce, so a
// bad value fails fast with an InvalidArgumentError that says what to fix,
// instead of producing a confusing 404 or 400 from a mangled URL.

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
	refNameMaxLength = 255
)

var (
	referencePattern = regexp.MustCompile(`^(` + refName + `)(?::(` + refTag + `))?(?:@(` + refDigest + `))?$`)
	refNamePattern   = regexp.MustCompile(`^` + refName + `$`)
)

const refFix = `use the form [registry[:port]/]repository[:tag|@digest] with a lowercase repository, for example "mongo:8" or "localhost:5000/team/mongo@sha256:..."`

// imageRef is a parsed image reference.
type imageRef struct {
	name   string // "mongo", "localhost:5000/team/mongo"
	tag    string // "latest" when neither tag nor digest was given
	digest string // "sha256:..." or ""
}

// parseImageRef validates and splits an image reference such as "mongo:8"
// or "localhost:5000/mongo@sha256:...". The repository part must be
// lowercase; only a registry host may contain uppercase letters.
func parseImageRef(ref string) (imageRef, error) {
	if strings.TrimSpace(ref) == "" {
		return imageRef{}, invalidArg("image reference", "", "the value is empty", refFix)
	}
	m := referencePattern.FindStringSubmatch(ref)
	if m == nil {
		lowered := strings.ToLower(strings.SplitN(strings.SplitN(ref, "@", 2)[0], ":", 2)[0])
		if refNamePattern.MatchString(lowered) {
			return imageRef{}, invalidArg("image reference", ref, "repository names must be lowercase", refFix)
		}
		return imageRef{}, invalidArg("image reference", ref, "it does not match the docker reference grammar", refFix)
	}
	r := imageRef{name: m[1], tag: m[2], digest: m[3]}
	if len(r.name) > refNameMaxLength {
		return imageRef{}, invalidArg("image reference", ref, "the repository name is longer than 255 characters", "shorten the repository path")
	}
	if r.tag == "" && r.digest == "" {
		r.tag = "latest"
	}
	return r, nil
}

// checkID trims and validates an object id or name for use in a URL path.
// A leading "/" (inspect reports names that way) is removed.
func checkID(kind, id string) (string, error) {
	trimmed := strings.TrimPrefix(strings.TrimSpace(id), "/")
	if trimmed == "" {
		return "", invalidArg(kind+" id", "", "the value is empty", "pass the id returned by ContainerCreate or ExecCreate, or the container name")
	}
	if !idPattern.MatchString(trimmed) {
		return "", invalidArg(kind+" id", id, "only letters, digits, '_', '.' and '-' are allowed", "pass the id returned by ContainerCreate or ExecCreate, or the container name")
	}
	return trimmed, nil
}

// checkContainerName validates a user-chosen container name. Empty is
// allowed (the daemon generates one).
func checkContainerName(name string) (string, error) {
	trimmed := strings.TrimPrefix(strings.TrimSpace(name), "/")
	if trimmed == "" {
		return "", nil
	}
	if !containerNamePattern.MatchString(trimmed) {
		return "", invalidArg("container name", name, "names need at least two characters from [a-zA-Z0-9][a-zA-Z0-9_.-]", `choose a name like "mongotest-1a2b" or pass "" to let the daemon pick one`)
	}
	return trimmed, nil
}

// checkImageRefOrID accepts an image reference or an image id for inspect.
func checkImageRefOrID(ref string) (string, error) {
	trimmed := strings.TrimSpace(ref)
	if imageIDPattern.MatchString(trimmed) {
		return trimmed, nil
	}
	if _, err := parseImageRef(trimmed); err != nil {
		return "", err
	}
	return trimmed, nil
}

// checkPortKey validates a "port[/proto]" key as used in ExposedPorts and
// PortBindings.
func checkPortKey(field, key string) error {
	port, proto, _ := strings.Cut(key, "/")
	n, err := strconv.Atoi(port)
	if err != nil || n < 1 || n > 65535 {
		return invalidArg(field, key, "the port must be a number from 1 to 65535", `use keys like "27017/tcp"`)
	}
	switch strings.ToLower(proto) {
	case "", "tcp", "udp", "sctp":
		return nil
	}
	return invalidArg(field, key, "the protocol must be tcp, udp or sctp", `use keys like "27017/tcp"`)
}

// checkHostPort validates a PortBinding.HostPort: empty (daemon assigns),
// a single port, or a "low-high" range.
func checkHostPort(field, hp string) error {
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
		return invalidArg(field, hp, "the host port must be a number from 0 to 65535", `use "" to let the daemon choose, "34819" to pin a port, or "40000-40010" for a range`)
	}
	if isRange {
		h, ok := parse(hi)
		if !ok || h < l {
			return invalidArg(field, hp, "the host port range must be low-high with low <= high", `use "40000-40010"`)
		}
	}
	return nil
}

// validate checks a ContainerConfig before it is sent.
func (cfg ContainerConfig) validate() error {
	if strings.TrimSpace(cfg.Image) == "" {
		return invalidArg("ContainerConfig.Image", "", "the image is required", `set Image to a reference such as "mongo:8"`)
	}
	if _, err := checkImageRefOrID(cfg.Image); err != nil {
		return err
	}
	for i, arg := range cfg.Cmd {
		if arg == "" {
			return invalidArg("ContainerConfig.Cmd["+strconv.Itoa(i)+"]", "", "an empty argument was given", "remove the empty element; the image entrypoint prepends mongod, so pass flags only")
		}
	}
	for k := range cfg.Labels {
		if strings.TrimSpace(k) == "" {
			return invalidArg("ContainerConfig.Labels", "", "a label has an empty key", `use keys like "mongotest"`)
		}
	}
	for k := range cfg.ExposedPorts {
		if err := checkPortKey("ContainerConfig.ExposedPorts key", k); err != nil {
			return err
		}
	}
	if cfg.HostConfig != nil {
		for k, bindings := range cfg.HostConfig.PortBindings {
			if err := checkPortKey("HostConfig.PortBindings key", k); err != nil {
				return err
			}
			field := "HostConfig.PortBindings[" + k + "]"
			for _, b := range bindings {
				if b.HostIP != "" && net.ParseIP(b.HostIP) == nil {
					return invalidArg(field+".HostIP", b.HostIP, "it is not an IP address", `use "127.0.0.1" to publish on loopback only, or "" for all interfaces`)
				}
				if err := checkHostPort(field+".HostPort", b.HostPort); err != nil {
					return err
				}
			}
		}
	}
	return nil
}

// validate checks an ExecConfig against the negotiated API version.
func (cfg ExecConfig) validate(apiVersion string) error {
	if len(cfg.Cmd) == 0 || strings.TrimSpace(cfg.Cmd[0]) == "" {
		return ErrNoCommand
	}
	for _, e := range cfg.Env {
		if !strings.Contains(e, "=") || strings.HasPrefix(e, "=") {
			return invalidArg("ExecConfig.Env entry", e, "it is not in KEY=value form", `use entries like "MONGO_INITDB_DATABASE=test"`)
		}
	}
	if len(cfg.Env) > 0 && compareVersions(apiVersion, "1.25") < 0 {
		return &APIVersionError{Feature: "ExecConfig.Env", Required: "1.25", Negotiated: apiVersion}
	}
	if cfg.WorkingDir != "" {
		if !strings.HasPrefix(cfg.WorkingDir, "/") {
			return invalidArg("ExecConfig.WorkingDir", cfg.WorkingDir, "it must be an absolute path inside the container", `use "/tmp" or another absolute path`)
		}
		if compareVersions(apiVersion, "1.35") < 0 {
			return &APIVersionError{Feature: "ExecConfig.WorkingDir", Required: "1.35", Negotiated: apiVersion}
		}
	}
	return nil
}
