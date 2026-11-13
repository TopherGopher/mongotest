package mongod

import (
	"strings"

	"github.com/tophergopher/mongotest/dockerclient"
)

// The registries, repositories and versions the known images are built from.
const (
	// RegistryDockerHub is empty because Docker Hub is where a reference with
	// no registry is pulled from.
	RegistryDockerHub = ""
	// RegistryPublicECR mirrors the Docker official images without Docker
	// Hub's pull rate limits.
	RegistryPublicECR = "public.ecr.aws"

	// RepositoryOfficial is the Docker official MongoDB image.
	RepositoryOfficial = "mongo"
	// RepositoryPublicECRMirror is the official image on public ECR.
	RepositoryPublicECRMirror = "docker/library/mongo"
	// RepositoryCommunity is MongoDB's own build of the community server.
	RepositoryCommunity = "mongodb/mongodb-community-server"
	// RepositoryEnterprise is MongoDB's enterprise server, which needs a
	// licence beyond evaluation.
	RepositoryEnterprise = "mongodb/mongodb-enterprise-server"
	// RepositoryAtlasLocal is Atlas Local: mongod with Atlas Search.
	RepositoryAtlasLocal = "mongodb/mongodb-atlas-local"

	// DefaultVersion is the default tag where bare version tags exist.
	DefaultVersion = "8"
	// DefaultVersionUBI is the default for MongoDB's own builds, whose tags
	// all name an OS variant.
	DefaultVersionUBI = "8.0-ubi9"

	// digestPrefix marks a Version that is a digest rather than a tag.
	digestPrefix = "sha256:"
)

// The known images. Derive from one with its With methods, which return a
// copy; do not assign to their fields.
//
//	mongod.ImageCommunity.WithVersion("8.0-ubi8")
//	mongod.ImageDockerHub.WithRegistry("mirror.corp.example")
var (
	// ImageDockerHub is the Docker official image, and the default.
	ImageDockerHub = MongoImage{Registry: RegistryDockerHub, Repository: RepositoryOfficial, Version: DefaultVersion}
	// ImagePublicECR is the same image from public ECR.
	ImagePublicECR = MongoImage{Registry: RegistryPublicECR, Repository: RepositoryPublicECRMirror, Version: DefaultVersion}
	// ImageCommunity is MongoDB's community server. It has no bare version
	// tags: "8" does not exist, "8.0-ubi9" does.
	ImageCommunity = MongoImage{Registry: RegistryDockerHub, Repository: RepositoryCommunity, Version: DefaultVersionUBI}
	// ImageEnterprise is MongoDB's enterprise server, tagged like community.
	ImageEnterprise = MongoImage{Registry: RegistryDockerHub, Repository: RepositoryEnterprise, Version: DefaultVersionUBI}
	// ImageAtlasLocal is Atlas Local, for Atlas Search and Vector Search.
	//
	// It runs its own single-node replica set and takes no mongod flags, so
	// WithReplicaSet and WithMongodArgs are refused for it.
	ImageAtlasLocal = MongoImage{Registry: RegistryDockerHub, Repository: RepositoryAtlasLocal, Version: DefaultVersion}
)

// MongoImage is an image in three parts, so one can change without restating
// the others. Any part may be empty; Resolve fills it in.
//
//	mongod.NewMongoImage("public.ecr.aws", "docker/library/mongo", "8")  // from parts
//	mongod.MustParseMongoImage("public.ecr.aws/docker/library/mongo:8")  // from a constant
//	mongod.ParseMongoImage(os.Getenv("IMAGE"))                           // from input
type MongoImage struct {
	// Registry is the host to pull from. Empty means Docker Hub.
	Registry string
	// Repository is the path within the registry, such as "mongo".
	Repository string
	// Version is a tag such as "8.0", or a digest such as "sha256:...".
	Version string
}

// NewMongoImage assembles an image from its parts.
func NewMongoImage(registry, repository, version string) MongoImage {
	return MongoImage{Registry: registry, Repository: repository, Version: version}
}

// MustParseMongoImage is ParseMongoImage for a reference written in the
// source, and panics if it is invalid. Use ParseMongoImage for input.
func MustParseMongoImage(ref string) MongoImage {
	img, err := ParseMongoImage(ref)
	if err != nil {
		panic(err)
	}
	return img
}

// WithRegistry returns a copy pulled from a different registry.
func (m MongoImage) WithRegistry(registry string) MongoImage {
	m.Registry = registry
	return m
}

// WithRepository returns a copy with a different repository.
func (m MongoImage) WithRepository(repository string) MongoImage {
	m.Repository = repository
	return m
}

// WithVersion returns a copy at a different tag or digest.
func (m MongoImage) WithVersion(version string) MongoImage {
	m.Version = version
	return m
}

// Reference renders the image the way docker spells it.
//
//	mongo:8
//	public.ecr.aws/docker/library/mongo:8.0
//	mongo@sha256:9f2b5c8e...   // a digest is joined with @
func (m MongoImage) Reference() string {
	name := m.Repository
	if m.Registry != "" {
		name = m.Registry + "/" + m.Repository
	}
	switch {
	case name == "":
		return m.Version // only a version so far, as WithImage("8.0") leaves it
	case m.Version == "":
		return name
	case m.PinnedByDigest():
		return name + "@" + m.Version
	default:
		return name + ":" + m.Version
	}
}

// String is Reference, so an image prints as one.
func (m MongoImage) String() string { return m.Reference() }

// IsZero reports whether no part is set.
func (m MongoImage) IsZero() bool { return m == MongoImage{} }

// PinnedByDigest reports whether Version is a digest rather than a tag.
func (m MongoImage) PinnedByDigest() bool { return strings.HasPrefix(m.Version, digestPrefix) }

// BareVersionTags reports whether the repository publishes tags that are
// only a version, such as "8". MongoDB's own server builds do not.
func (m MongoImage) BareVersionTags() bool { return !m.isMongoDBIncBuild() }

// AcceptsMongodArgs reports whether a container command reaches mongod as
// flags. It does not in Atlas Local, where it would replace the program.
func (m MongoImage) AcceptsMongodArgs() bool { return !m.is(RepositoryAtlasLocal) }

// ReplicaSetPreconfigured reports whether the image runs a replica set of
// its own, as Atlas Local does. Its name has to be read from the server.
func (m MongoImage) ReplicaSetPreconfigured() bool { return m.is(RepositoryAtlasLocal) }

// DataDir is where mongod keeps its data inside the container.
func (m MongoImage) DataDir() string { return "/data/db" }

// isMongoDBIncBuild reports whether this is one of MongoDB's own server
// builds, whose tags all name an OS variant.
func (m MongoImage) isMongoDBIncBuild() bool {
	return m.is(RepositoryCommunity) || m.is(RepositoryEnterprise)
}

// is reports whether this is a known image: its repository is the known
// one, or ends in it after a slash, which is how a registry proxy serves it.
func (m MongoImage) is(repository string) bool {
	return m.Repository == repository || strings.HasSuffix(m.Repository, "/"+repository)
}

// versionHint is the example tag an error shows for this repository.
func (m MongoImage) versionHint() string { return defaultVersionFor(m.Repository) }

// defaultVersionFor returns the version a repository gets when none is given.
func defaultVersionFor(repository string) string {
	if (MongoImage{Repository: repository}).isMongoDBIncBuild() {
		return DefaultVersionUBI
	}
	return DefaultVersion
}

// ParseMongoImage splits a reference into its parts. Anything missing stays
// empty, for Resolve to fill in.
//
//	"8.0-ubi9"                               version
//	"mongo"                                  repository
//	"mongo:8.0"                              repository and version
//	"public.ecr.aws/docker/library/mongo"    registry and repository
//	"localhost:5000/team/mongo:8"            registry with a port
//	"mongo@sha256:..."                       pinned by digest
//
// A lone word is a version if it starts with a digit or is "latest".
func ParseMongoImage(ref string) (MongoImage, error) {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return MongoImage{}, ErrEmptyImage
	}
	if isLoneVersion(ref) {
		if err := checkVersion(ref); err != nil {
			return MongoImage{}, err
		}
		return MongoImage{Version: ref}, nil
	}
	// Everything else is a docker reference, parsed by the same rules the
	// client validates with.
	parsed, err := dockerclient.ParseImageRef(ref)
	if err != nil {
		return MongoImage{}, err
	}
	registry, repository := splitRegistry(parsed.Name)
	return MongoImage{Registry: registry, Repository: repository, Version: versionOf(ref, parsed)}, nil
}

// isLoneVersion reports whether a reference is a version and nothing else.
func isLoneVersion(ref string) bool {
	if strings.ContainsAny(ref, "/:@") {
		return false
	}
	return ref == "latest" || isDigit(rune(ref[0]))
}

// versionOf returns the digest or tag a reference spells out, or "" if it
// names neither. ParseImageRef fills in "latest" for a missing tag, which
// would hide that the caller left the version open.
func versionOf(ref string, parsed dockerclient.ImageRef) string {
	if parsed.Digest != "" {
		return parsed.Digest
	}
	// A colon before the last slash is a registry port, not a tag.
	lastElement := ref[strings.LastIndex(ref, "/")+1:]
	if strings.Contains(lastElement, ":") {
		return parsed.Tag
	}
	return ""
}

// splitRegistry separates a registry host from the repository. By docker's
// rule the first element is a host if it has a dot or a colon, or is
// "localhost".
func splitRegistry(name string) (registry, repository string) {
	first, rest, found := strings.Cut(name, "/")
	if !found {
		return "", name
	}
	if strings.ContainsAny(first, ".:") || first == "localhost" {
		return first, rest
	}
	return "", name
}

// checkVersion rejects a version that could not be a docker tag.
func checkVersion(version string) error {
	if _, err := dockerclient.ParseImageRef(RepositoryOfficial + ":" + version); err != nil {
		return dockerclient.InvalidArgument("image version", version,
			"it is not a valid docker tag",
			`pass a tag such as "8", "8.0.30" or "8.0-ubi9"`)
	}
	return nil
}
