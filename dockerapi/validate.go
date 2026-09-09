package dockerapi

import (
	"errors"
	"fmt"
	"net"
	"regexp"
	"strconv"
	"strings"
)

// Input validation mirrors what the docker CLI and the daemon enforce, so a
// bad value fails fast with a clear message instead of producing a confusing
// 404 or 400 from a mangled URL.

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

// imageRef is a parsed image reference.
type imageRef struct {
	name   string // "mongo", "localhost:5000/team/mongo"
	tag    string // "latest" when neither tag nor digest was given
	digest string // "sha256:..." or ""
}

// parseImageRef validates and splits an image reference such as "mongo:8",
// "localhost:5000/mongo@sha256:...". The repository part must be lowercase;
// only a registry host may contain uppercase letters.
func parseImageRef(ref string) (imageRef, error) {
	if strings.TrimSpace(ref) == "" {
		return imageRef{}, errors.New("dockerapi: image reference is empty")
	}
	m := referencePattern.FindStringSubmatch(ref)
	if m == nil {
		if refNamePattern.MatchString(strings.ToLower(strings.SplitN(strings.SplitN(ref, "@", 2)[0], ":", 2)[0])) {
			return imageRef{}, fmt.Errorf("dockerapi: invalid image reference %q: repository name must be lowercase", ref)
		}
		return imageRef{}, fmt.Errorf("dockerapi: invalid image reference %q", ref)
	}
	r := imageRef{name: m[1], tag: m[2], digest: m[3]}
	if len(r.name) > refNameMaxLength {
		return imageRef{}, fmt.Errorf("dockerapi: invalid image reference %q: repository name longer than %d characters", ref, refNameMaxLength)
	}
	if r.tag == "" && r.digest == "" {
		r.tag = "latest"
	}
	return r, nil
}

// checkID trims and validates an object id or name for use in a URL path.
// A leading "/" (inspect reports names that way) is removed.
func checkID(kind, id string) (string, error) {
	id = strings.TrimSpace(id)
	id = strings.TrimPrefix(id, "/")
	if id == "" {
		return "", fmt.Errorf("dockerapi: %s id is empty", kind)
	}
	if !idPattern.MatchString(id) {
		return "", fmt.Errorf("dockerapi: invalid %s id %q", kind, id)
	}
	return id, nil
}

// checkContainerName validates a user-chosen container name. Empty is
// allowed (the daemon generates one).
func checkContainerName(name string) (string, error) {
	name = strings.TrimPrefix(strings.TrimSpace(name), "/")
	if name == "" {
		return "", nil
	}
	if !containerNamePattern.MatchString(name) {
		return "", fmt.Errorf("dockerapi: invalid container name %q: must be at least two characters from [a-zA-Z0-9][a-zA-Z0-9_.-]", name)
	}
	return name, nil
}

// checkImageRefOrID accepts an image reference or an image id for inspect.
func checkImageRefOrID(ref string) (string, error) {
	ref = strings.TrimSpace(ref)
	if imageIDPattern.MatchString(ref) {
		return ref, nil
	}
	if _, err := parseImageRef(ref); err != nil {
		return "", err
	}
	return ref, nil
}

// checkPortKey validates a "port[/proto]" key as used in ExposedPorts and
// PortBindings.
func checkPortKey(key string) error {
	port, proto, _ := strings.Cut(key, "/")
	n, err := strconv.Atoi(port)
	if err != nil || n < 1 || n > 65535 {
		return fmt.Errorf("dockerapi: invalid port %q in %q (want 1-65535)", port, key)
	}
	switch strings.ToLower(proto) {
	case "", "tcp", "udp", "sctp":
		return nil
	}
	return fmt.Errorf("dockerapi: invalid protocol %q in %q (want tcp, udp or sctp)", proto, key)
}

// checkHostPort validates a PortBinding.HostPort: empty (daemon assigns),
// a single port, or a "low-high" range.
func checkHostPort(hp string) error {
	if hp == "" {
		return nil
	}
	lo, hi, isRange := strings.Cut(hp, "-")
	parse := func(s string) (int, error) {
		n, err := strconv.Atoi(s)
		if err != nil || n < 0 || n > 65535 {
			return 0, fmt.Errorf("dockerapi: invalid host port %q (want 0-65535)", s)
		}
		return n, nil
	}
	l, err := parse(lo)
	if err != nil {
		return err
	}
	if isRange {
		h, err := parse(hi)
		if err != nil {
			return err
		}
		if h < l {
			return fmt.Errorf("dockerapi: invalid host port range %q", hp)
		}
	}
	return nil
}

// validate checks a ContainerConfig before it is sent.
func (cfg ContainerConfig) validate() error {
	if strings.TrimSpace(cfg.Image) == "" {
		return errors.New("dockerapi: ContainerConfig.Image is required")
	}
	if _, err := checkImageRefOrID(cfg.Image); err != nil {
		return err
	}
	for i, arg := range cfg.Cmd {
		if arg == "" {
			return fmt.Errorf("dockerapi: ContainerConfig.Cmd[%d] is empty", i)
		}
	}
	for k := range cfg.Labels {
		if strings.TrimSpace(k) == "" {
			return errors.New("dockerapi: ContainerConfig.Labels has an empty key")
		}
	}
	for k := range cfg.ExposedPorts {
		if err := checkPortKey(k); err != nil {
			return fmt.Errorf("ExposedPorts: %w", err)
		}
	}
	if cfg.HostConfig != nil {
		for k, bindings := range cfg.HostConfig.PortBindings {
			if err := checkPortKey(k); err != nil {
				return fmt.Errorf("PortBindings: %w", err)
			}
			for _, b := range bindings {
				if b.HostIP != "" && net.ParseIP(b.HostIP) == nil {
					return fmt.Errorf("dockerapi: PortBindings[%q]: host ip %q is not an IP address", k, b.HostIP)
				}
				if err := checkHostPort(b.HostPort); err != nil {
					return fmt.Errorf("PortBindings[%q]: %w", k, err)
				}
			}
		}
	}
	return nil
}

// validate checks an ExecConfig against the negotiated API version.
func (cfg ExecConfig) validate(apiVersion string) error {
	if len(cfg.Cmd) == 0 || strings.TrimSpace(cfg.Cmd[0]) == "" {
		return errors.New("dockerapi: ExecConfig.Cmd must name a program")
	}
	for _, e := range cfg.Env {
		if !strings.Contains(e, "=") || strings.HasPrefix(e, "=") {
			return fmt.Errorf("dockerapi: ExecConfig.Env entry %q is not KEY=value", e)
		}
	}
	if len(cfg.Env) > 0 && compareVersions(apiVersion, "1.25") < 0 {
		return fmt.Errorf("dockerapi: exec Env requires API version 1.25 or newer; the daemon negotiated %s", apiVersion)
	}
	if cfg.WorkingDir != "" {
		if !strings.HasPrefix(cfg.WorkingDir, "/") {
			return fmt.Errorf("dockerapi: ExecConfig.WorkingDir %q must be an absolute container path", cfg.WorkingDir)
		}
		if compareVersions(apiVersion, "1.35") < 0 {
			return fmt.Errorf("dockerapi: exec WorkingDir requires API version 1.35 or newer; the daemon negotiated %s", apiVersion)
		}
	}
	return nil
}
