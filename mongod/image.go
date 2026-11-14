package mongod

import (
	"strings"

	"github.com/tophergopher/mongotest/dockerclient"
)

// The registries, repositories and versions the known images are built from.
const (
	// registryDockerHub is empty because Docker Hub is where a reference with
	// no registry is pulled from.
	registryDockerHub = ""
	// registryPublicECR mirrors the Docker official images without Docker
	// Hub's pull rate limits.
	registryPublicECR = "public.ecr.aws"

	// repositoryOfficial is the Docker official MongoDB image.
	repositoryOfficial = "mongo"
	// repositoryPublicECRMirror is the official image on public ECR.
	repositoryPublicECRMirror = "docker/library/mongo"
	// repositoryCommunity is MongoDB's own build of the community server.
	repositoryCommunity = "mongodb/mongodb-community-server"
	// repositoryEnterprise is MongoDB's enterprise server, which needs a
	// licence beyond evaluation.
	repositoryEnterprise = "mongodb/mongodb-enterprise-server"
	// repositoryAtlasLocal is Atlas Local: mongod with Atlas Search.
	repositoryAtlasLocal = "mongodb/mongodb-atlas-local"

	// defaultVersion is the default tag for a repository that publishes plain
	// version tags. It names a minor version so that every image family
	// defaults to the same server.
	defaultVersion = "8.0"
	// defaultVersionUBI is the default for MongoDB's own builds, whose tags
	// all name an OS variant.
	defaultVersionUBI = "8.0-ubi9"
)

// The known images. Derive from one with its With methods, which return a
// copy; do not assign to their fields.
//
//	mongod.ImageCommunity.WithVersion("8.0-ubi8")
//	mongod.ImageDockerHub.WithRegistry("mirror.corp.example")
var (
	// ImageDockerHub is the Docker official image, and the default.
	ImageDockerHub = MongoImage{Registry: registryDockerHub, Repository: repositoryOfficial, Version: defaultVersion}
	// ImagePublicECR is the same image from public ECR.
	ImagePublicECR = MongoImage{Registry: registryPublicECR, Repository: repositoryPublicECRMirror, Version: defaultVersion}
	// ImageCommunity is MongoDB's community server. It has no bare version
	// tags: "8" does not exist, "8.0-ubi9" does.
	ImageCommunity = MongoImage{Registry: registryDockerHub, Repository: repositoryCommunity, Version: defaultVersionUBI}
	// ImageEnterprise is MongoDB's enterprise server, tagged like community.
	ImageEnterprise = MongoImage{Registry: registryDockerHub, Repository: repositoryEnterprise, Version: defaultVersionUBI}
	// ImageAtlasLocal is Atlas Local, for Atlas Search and Vector Search.
	//
	// It runs its own single-node replica set and takes no mongod flags, so
	// WithReplicaSet and WithMongodArgs are refused for it.
	ImageAtlasLocal = MongoImage{Registry: registryDockerHub, Repository: repositoryAtlasLocal, Version: defaultVersion}
)

// MongoImage is an image in three parts, so one can change without restating
// the others. Any part may be empty; Resolve fills it in.
//
//	mongod.ImageCommunity.WithVersion("8.0-ubi8")                        // from a known image
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
	case m.pinnedByDigest():
		return name + "@" + m.Version
	default:
		return name + ":" + m.Version
	}
}

// String is Reference, so an image prints as one.
func (m MongoImage) String() string { return m.Reference() }

// pinnedByDigest reports whether Version is a digest rather than a tag. A
// digest has a colon after its algorithm, which a tag cannot contain.
func (m MongoImage) pinnedByDigest() bool { return strings.Contains(m.Version, ":") }

// ReplicaSetPreconfigured reports whether the image runs a replica set of
// its own, as Atlas Local does. Its name has to be read from the server.
func (m MongoImage) ReplicaSetPreconfigured() bool { return m.is(repositoryAtlasLocal) }

// acceptsMongodArgs reports whether a container command reaches mongod as
// flags. It does not in Atlas Local, where it would replace the program.
func (m MongoImage) acceptsMongodArgs() bool { return !m.is(repositoryAtlasLocal) }

// hasBareVersionTags reports whether the repository publishes tags that are
// only a version, such as "8.0". MongoDB's own builds do not: every tag
// there names an OS variant.
func (m MongoImage) hasBareVersionTags() bool {
	return !m.is(repositoryCommunity) && !m.is(repositoryEnterprise)
}

// defaultTag returns the version this repository gets when none is given.
func (m MongoImage) defaultTag() string {
	if m.hasBareVersionTags() {
		return defaultVersion
	}
	return defaultVersionUBI
}

// is reports whether this is a known image: its repository is the known
// one, or ends in it after a slash, which is how a registry proxy serves it.
func (m MongoImage) is(repository string) bool {
	return m.Repository == repository || strings.HasSuffix(m.Repository, "/"+repository)
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
	if _, err := dockerclient.ParseImageRef(repositoryOfficial + ":" + version); err != nil {
		return dockerclient.InvalidArgument("image version", version,
			"it is not a valid docker tag",
			`pass a tag such as "8", "8.0.30" or "8.0-ubi9"`)
	}
	return nil
}
