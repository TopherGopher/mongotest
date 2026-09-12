package mongod

import (
	"strings"

	"github.com/tophergopher/mongotest/dockerclient"
)

// Registries and repositories the well-known images below are built from.
// They are here as constants so that a caller can assemble a reference from
// parts without naming any string twice.
const (
	// RegistryDockerHub is the implicit registry: Docker Hub is what a
	// reference with no host resolves to, so the constant is empty rather
	// than "docker.io". Spelling it out would make docker.io/library/mongo
	// and mongo two different strings for the same image in every log line.
	RegistryDockerHub = ""
	// RegistryPublicECR is Amazon's public registry, which mirrors the Docker
	// official images. It is the usual way out of Docker Hub's pull rate
	// limits, which CI hits before a laptop does.
	RegistryPublicECR = "public.ecr.aws"

	// RepositoryOfficial is the Docker official MongoDB image.
	RepositoryOfficial = "mongo"
	// RepositoryPublicECRMirror is where the official image lives on public
	// ECR. Docker official images are mirrored under docker/library.
	RepositoryPublicECRMirror = "docker/library/mongo"
	// RepositoryCommunity is MongoDB's own build of the community server.
	RepositoryCommunity = "mongodb/mongodb-community-server"
	// RepositoryEnterprise is MongoDB's build of the enterprise server. It
	// needs a licence for anything beyond evaluation and development.
	RepositoryEnterprise = "mongodb/mongodb-enterprise-server"
	// RepositoryAtlasLocal is Atlas Local: mongod together with mongot, which
	// is what makes Atlas Search and Vector Search work locally. Read the
	// caveats on ImageAtlasLocal before choosing it.
	RepositoryAtlasLocal = "mongodb/mongodb-atlas-local"

	// DefaultVersion is the MongoDB major version this package targets for
	// the images that publish a bare major tag.
	DefaultVersion = "8"
	// DefaultVersionUBI is the equivalent for MongoDB's own server images,
	// which publish no bare tags: every tag names an OS variant.
	DefaultVersionUBI = "8.0-ubi9"
	// digestPrefix marks a Version that pins a content digest rather than
	// naming a tag.
	digestPrefix = "sha256:"
)

// The images this package knows about. Take one and change what you need:
//
//	mongod.ImageCommunity.WithVersion("8.0-ubi8")
//	mongod.ImageDockerHub.WithRegistry("mirror.corp.example")
//
// They are variables only because Go has no constant of struct type. Derive
// from them with the With methods, which return a copy; assigning to a field
// of one of these would change it for every test in the process.
var (
	// ImageDockerHub is the Docker official image, and the default.
	ImageDockerHub = MongoImage{Registry: RegistryDockerHub, Repository: RepositoryOfficial, Version: DefaultVersion}
	// ImagePublicECR is the same image from public ECR, for getting out from
	// under Docker Hub's pull rate limits.
	ImagePublicECR = MongoImage{Registry: RegistryPublicECR, Repository: RepositoryPublicECRMirror, Version: DefaultVersion}
	// ImageCommunity is MongoDB's own community server build. Its tags always
	// name an OS variant, so a bare version such as "8" does not exist there.
	ImageCommunity = MongoImage{Registry: RegistryDockerHub, Repository: RepositoryCommunity, Version: DefaultVersionUBI}
	// ImageEnterprise is MongoDB's enterprise server build, tagged like the
	// community one. It needs a licence for anything beyond evaluation.
	ImageEnterprise = MongoImage{Registry: RegistryDockerHub, Repository: RepositoryEnterprise, Version: DefaultVersionUBI}
	// ImageAtlasLocal is Atlas Local, which runs mongod and mongot together
	// so that Atlas Search and Vector Search work against a local container.
	//
	// It behaves differently from every other image here, in two ways that
	// Start enforces rather than letting them fail confusingly:
	//
	//   - It already runs a single-node replica set, under a name it
	//     generates from the container's hostname. WithReplicaSet is refused:
	//     there is nothing to ask for, and the name cannot be chosen.
	//   - Its Cmd is its entrypoint ("/usr/local/bin/runner server") and it
	//     has no ENTRYPOINT of its own, so mongod flags passed as a command
	//     replace the program. WithMongodArgs is refused, because the
	//     container would die with exit code 127 and an exec error naming the
	//     flag, which explains nothing.
	//
	// There is a third difference that cannot be enforced, only expected: it
	// restarts mongod during its own startup. Measured on 8.0.28, mongod came
	// up and served from about six seconds in, went away at eighteen, and a
	// different process was serving by twenty. Start's readiness can therefore
	// return while the first of those is running, and a client that connects
	// immediately will see one disconnection. Retry, which a driver layer
	// pinging with backoff already does.
	ImageAtlasLocal = MongoImage{Registry: RegistryDockerHub, Repository: RepositoryAtlasLocal, Version: DefaultVersion}
)

// MongoImage is a MongoDB image in the three parts a caller thinks in: where
// it is pulled from, what it is called there, and which version.
//
// Keeping them apart is what lets one part be changed without restating the
// others, whether from an option or from an environment variable: a CI job
// that only needs to move off Docker Hub sets the registry and keeps the
// version the tests already pin.
//
// There are three ways to get one, depending on what you are holding:
//
//	mongod.NewMongoImage("public.ecr.aws", "docker/library/mongo", "8")  // parts
//	mongod.MustParseMongoImage("public.ecr.aws/docker/library/mongo:8")  // a constant string
//	mongod.ParseMongoImage(os.Getenv("IMAGE"))                           // a string that might be wrong
//
// and [Options.WithImage] takes the same strings [ParseMongoImage] does, so
// nothing has to be assembled by hand to start a container from a reference
// someone pasted.
type MongoImage struct {
	// Registry is the host to pull from, such as "public.ecr.aws" or
	// "123456789012.dkr.ecr.eu-west-1.amazonaws.com". Empty means Docker Hub.
	Registry string
	// Repository is the path within the registry: "mongo",
	// "mongodb/mongodb-community-server", "platform/mongo".
	Repository string
	// Version is the tag, such as "8", "8.0.30" or "8.0-ubi9". It may instead
	// be a content digest ("sha256:..."), which pins the image exactly; a
	// reference is then rendered with @ in place of : as docker requires.
	Version string
}

// NewMongoImage assembles an image from its three parts. Any part may be
// empty: an empty registry means Docker Hub, and an empty repository or
// version is filled in during resolution from the default, or from whichever
// well-known image the other parts name.
//
// To go the other way, from a whole reference to its parts, use
// [MustParseMongoImage] or [ParseMongoImage].
func NewMongoImage(registry, repository, version string) MongoImage {
	return MongoImage{Registry: registry, Repository: repository, Version: version}
}

// MustParseMongoImage is [ParseMongoImage] for a reference that is known good
// at the point it is written, and panics instead of returning an error.
//
// It is for the places an error has nowhere to go: a package-level variable, a
// struct literal, a test table.
//
//	var hardened = mongod.MustParseMongoImage("1234.dkr.ecr.eu-west-1.amazonaws.com/platform/mongo:8.0-hardened")
//
// A reference that comes from configuration or from a user is not known good,
// so use [ParseMongoImage] for those and report what it says. Panicking there
// would turn a typo in an environment variable into a crash with no context.
func MustParseMongoImage(ref string) MongoImage {
	img, err := ParseMongoImage(ref)
	if err != nil {
		panic(err)
	}
	return img
}

// WithRegistry returns a copy pulled from a different registry. This is how a
// known image becomes a private mirror of itself.
func (m MongoImage) WithRegistry(registry string) MongoImage {
	m.Registry = registry
	return m
}

// WithRepository returns a copy with a different repository path.
func (m MongoImage) WithRepository(repository string) MongoImage {
	m.Repository = repository
	return m
}

// WithVersion returns a copy at a different version. It accepts a tag or a
// digest.
func (m MongoImage) WithVersion(version string) MongoImage {
	m.Version = version
	return m
}

// Reference renders the image the way docker spells it, which is what is sent
// to the daemon and what appears in log lines and error messages.
//
//	mongo:8
//	public.ecr.aws/docker/library/mongo:8.0
//	mongo@sha256:9f2b5c8e...   // when Version pins a digest
func (m MongoImage) Reference() string {
	name := m.Repository
	if m.Registry != "" {
		name = m.Registry + "/" + m.Repository
	}
	switch {
	case m.Version == "":
		return name
	case strings.HasPrefix(m.Version, digestPrefix):
		// A digest is joined with @; using : would be a tag called "sha256".
		return name + "@" + m.Version
	default:
		return name + ":" + m.Version
	}
}

// String is Reference, so an image can be printed or logged directly.
func (m MongoImage) String() string { return m.Reference() }

// IsZero reports whether no part of the image has been set, which is how
// resolution tells "the caller said nothing" from "the caller chose this".
//
//	true    // MongoImage{}
//	false   // anything with a registry, repository or version
func (m MongoImage) IsZero() bool {
	return m.Registry == "" && m.Repository == "" && m.Version == ""
}

// PinnedByDigest reports whether Version pins a content digest rather than
// naming a tag. A digest is exact: the same bytes every run, on every machine.
//
//	false   // Version is "8"
//	true    // Version is "sha256:9f2b5c8e..."
func (m MongoImage) PinnedByDigest() bool { return strings.HasPrefix(m.Version, digestPrefix) }

// BareVersionTags reports whether this image's registry publishes tags that
// are just a version, such as "8" or "8.0.30".
//
//	true    // ImageDockerHub, ImagePublicECR, ImageAtlasLocal
//	false   // ImageCommunity, ImageEnterprise
//
// It is false for MongoDB's own server builds, whose every tag names an OS
// variant ("8.0-ubi9", "8.0.30-ubuntu2204"): applying a bare version to one
// of those produces a reference that does not exist, and a pull that fails
// with a 404 saying nothing about why. An image this package does not know is
// assumed to publish them, which is the permissive answer and the right one
// for a private mirror.
func (m MongoImage) BareVersionTags() bool {
	switch m.Repository {
	case RepositoryCommunity, RepositoryEnterprise:
		return false
	default:
		return true
	}
}

// AcceptsMongodArgs reports whether arguments given as the container's command
// reach mongod.
//
//	true    // every image here except one
//	false   // ImageAtlasLocal
//
// It is false for Atlas Local, whose Cmd is its entrypoint: arguments replace
// the program rather than being passed to it. An unknown image is assumed to
// behave like the official one rather than being refused options it probably
// accepts.
func (m MongoImage) AcceptsMongodArgs() bool { return m.Repository != RepositoryAtlasLocal }

// ReplicaSetPreconfigured reports whether the image brings up a replica set of
// its own.
//
//	false   // a standalone server until --replSet says otherwise
//	true    // ImageAtlasLocal
//
// It is true only for Atlas Local, which initiates a single-node set under a
// name generated from the container's hostname. A driver layer talking to one
// has to read the name out of the server rather than assume it.
func (m MongoImage) ReplicaSetPreconfigured() bool { return m.Repository == RepositoryAtlasLocal }

// DataDir is the path inside the container that mongod stores its data in.
//
//	/data/db
//
// Every image here uses the same one; it is a method so that it stays correct if
// that ever stops being true.
func (m MongoImage) DataDir() string { return "/data/db" }

// versionHint is the example tag an error message shows when a bare version
// was applied to a repository that publishes none.
func (m MongoImage) versionHint() string {
	if m.BareVersionTags() {
		return DefaultVersion
	}
	return DefaultVersionUBI
}

// defaultVersionFor returns the version a repository gets when none was
// asked for.
func defaultVersionFor(repository string) string {
	switch repository {
	case RepositoryCommunity, RepositoryEnterprise:
		return DefaultVersionUBI
	default:
		return DefaultVersion
	}
}

// ParseMongoImage reads an image from a string in any of the shapes a caller
// is likely to have one in, and splits it into its three parts.
//
//	"8"                                      version only
//	"8.0-ubi9"                               version only, with an OS variant
//	"latest"                                 version only
//	"mongo"                                  repository only
//	"mongodb/mongodb-atlas-local"            repository only, with an organisation
//	"mongo:8.0"                              repository and version
//	"public.ecr.aws/docker/library/mongo"    registry and repository
//	"1234.dkr.ecr.eu-west-1.amazonaws.com/platform/mongo:8.0-hardened"
//	"localhost:5000/team/mongo:8"            a registry with a port
//	"mongo@sha256:..."                       pinned by digest
//
// Whatever is missing is filled in during resolution, so naming a version
// alone keeps the default repository and naming a repository alone gets that
// repository's own default version.
//
// Use [MustParseMongoImage] where the reference is written in the source and an
// error has nowhere to go.
//
// Two rules do the work. The first element is a registry host when it
// contains a dot or a colon, or is exactly "localhost" -- the same rule
// docker uses, and what keeps "localhost:5000/team/mongo" from being read as
// a repository with a tag. A single element with no slash is a version when
// it starts with a digit or is "latest", and a repository otherwise.
func ParseMongoImage(ref string) (MongoImage, error) {
	trimmed := strings.TrimSpace(ref)
	if trimmed == "" {
		return MongoImage{}, ErrEmptyImage
	}

	// A lone version, which is the commonest thing to pass and the only shape
	// that is not a reference at all.
	if !strings.ContainsAny(trimmed, "/:@") && looksLikeVersion(trimmed) {
		if err := checkVersion(trimmed); err != nil {
			return MongoImage{}, err
		}
		return MongoImage{Version: trimmed}, nil
	}

	// Everything else is a docker reference, so the grammar is already
	// written down in dockerclient and is the same one the daemon applies.
	// Parse it there rather than inventing a second, subtly different one.
	name, version, err := splitReference(trimmed)
	if err != nil {
		return MongoImage{}, err
	}
	registry, repository := splitRegistry(name)
	return MongoImage{Registry: registry, Repository: repository, Version: version}, nil
}

// splitReference separates a reference's name from its tag or digest, using
// the shared validator so that anything this accepts the daemon will too.
func splitReference(ref string) (name, version string, err error) {
	// ParseImageRef defaults a missing tag to "latest". Here a missing
	// version has to stay missing, so that resolution can fill it in from the
	// repository's own default instead.
	parsed, err := dockerclient.ParseImageRef(ref)
	if err != nil {
		return "", "", err
	}
	switch {
	case parsed.Digest != "":
		return parsed.Name, parsed.Digest, nil
	case hasExplicitTag(ref):
		return parsed.Name, parsed.Tag, nil
	default:
		return parsed.Name, "", nil
	}
}

// hasExplicitTag reports whether a reference spells a tag out, as opposed to
// ParseImageRef having defaulted one to "latest". A colon in the first element
// is a registry port rather than a tag, which is why this cannot just look
// for a colon.
func hasExplicitTag(ref string) bool {
	lastSlash := strings.LastIndex(ref, "/")
	return strings.Contains(ref[lastSlash+1:], ":")
}

// splitRegistry separates a registry host from the repository path. The first
// element is a host when it contains a dot or a colon, or is exactly
// "localhost"; this is docker's own rule.
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

// looksLikeVersion reports whether a lone element is a version rather than a
// repository name. Versions start with a digit ("8", "8.0-ubi9"), and
// "latest" is the one that does not.
func looksLikeVersion(s string) bool {
	if s == "latest" {
		return true
	}
	return s[0] >= '0' && s[0] <= '9'
}

// checkVersion rejects a version that could not be a tag, so that the failure
// names the value rather than arriving later as a mangled reference.
func checkVersion(version string) error {
	if _, err := dockerclient.ParseImageRef(RepositoryOfficial + ":" + version); err != nil {
		return dockerclient.InvalidArgument("image version", version,
			"it is not a valid docker tag",
			`pass a tag such as "8", "8.0.30" or "8.0-ubi9"`)
	}
	return nil
}
