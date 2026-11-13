package mongod

import (
	"archive/tar"
	"bytes"
	"context"
	"strconv"
	"strings"

	"github.com/tophergopher/mongotest/dockerclient"
)

// Where the replica set key is put inside the container. /etc exists in every
// image, and nothing else in one is called this.
const (
	keyFileDir  = "/etc"
	keyFileName = "mongotest-keyfile"
	keyFilePath = keyFileDir + "/" + keyFileName
)

// The users mongod runs as, for an image that names its user rather than
// numbering it. Measured with id(1): the Docker official image starts as root
// and drops to mongodb, which it creates as 999; MongoDB's own builds, in
// their ubi8, ubi9 and ubuntu2204 variants, declare a user that is 1000.
const (
	officialMongodUID = 999
	mongodbIncUID     = 1000
)

// copyKeyFile puts a fresh replica set key into a container that has been
// created and not yet started, for mongod to be pointed at with --keyFile.
//
// mongod is particular about the file: it refuses one that group or other can
// read, so the file has to belong to the user mongod runs as, and that user is
// not root. The archive therefore carries the owner itself rather than going
// through CopyToContainer, which writes everything as root. A key owned by the
// wrong user ends the container with "error opening file ... bad file" in its
// log.
//
// The key is used by nothing but this one member, so it is generated here and
// kept nowhere.
func copyKeyFile(ctx context.Context, docker dockerclient.Client, id, imageRef string) error {
	image, err := docker.ImageInspect(ctx, imageRef)
	if err != nil {
		return err
	}
	owner := keyFileOwner(image.Config.User)
	key := randomHex(secretRandomBytes)

	var archive bytes.Buffer
	tw := tar.NewWriter(&archive)
	header := &tar.Header{
		Name: keyFileName, Typeflag: tar.TypeReg, Mode: 0o400,
		Uid: owner, Gid: owner, Size: int64(len(key)),
	}
	if err := tw.WriteHeader(header); err != nil {
		return err
	}
	if _, err := tw.Write([]byte(key)); err != nil {
		return err
	}
	if err := tw.Close(); err != nil {
		return err
	}
	return docker.CopyArchiveToContainer(ctx, id, keyFileDir, &archive)
}

// keyFileOwner works out the numeric user mongod runs as from the user the
// image declares, which is what survives an image being mirrored under a
// different name:
//
//	""          999    the official image: root, which its entrypoint drops to mongodb
//	"1000:0"    1000   a number is taken as given
//	"mongod"    1000   a name, which is what MongoDB's own builds declare
//
// A name cannot be looked up without running something in the image, so it
// is assumed to be MongoDB's. An image that names some other user needs its
// mongod started without a key file: WithoutAuth, or no replica set.
func keyFileOwner(imageUser string) int {
	user, _, _ := strings.Cut(imageUser, ":")
	if user == "" {
		return officialMongodUID
	}
	if uid, err := strconv.Atoi(user); err == nil {
		return uid
	}
	return mongodbIncUID
}
