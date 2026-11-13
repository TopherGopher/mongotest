package mongod

import (
	"archive/tar"
	"bytes"
	"context"
	"strconv"
	"strings"

	"github.com/tophergopher/mongotest/dockerclient"
)

// Where the replica set key goes inside the container.
const (
	keyFileDir  = "/etc"
	keyFileName = "mongotest-keyfile"
	keyFilePath = keyFileDir + "/" + keyFileName
)

// The numeric user mongod runs as in each family of image.
const (
	// officialMongodUID is the mongodb user of the Docker official image.
	officialMongodUID = 999
	// mongodbIncUID is the user MongoDB's own builds declare.
	mongodbIncUID = 1000
)

// copyKeyFile puts a new replica set key into a container that is created
// but not started, for mongod to read with --keyFile.
//
// mongod refuses a key that group or other can read, so the file must belong
// to the user mongod runs as. The archive is built here to carry that owner;
// CopyToContainer writes everything as root.
func copyKeyFile(ctx context.Context, docker dockerclient.Client, id, imageRef string) error {
	image, err := docker.ImageInspect(ctx, imageRef)
	if err != nil {
		return err
	}
	archive, err := keyFileArchive(keyFileOwner(image.Config.User), randomHex(secretRandomBytes))
	if err != nil {
		return err
	}
	return docker.CopyArchiveToContainer(ctx, id, keyFileDir, archive)
}

// keyFileArchive returns a tar archive holding the key, readable only by
// its owner.
func keyFileArchive(owner int, key string) (*bytes.Buffer, error) {
	var archive bytes.Buffer
	tw := tar.NewWriter(&archive)
	header := &tar.Header{
		Name: keyFileName, Typeflag: tar.TypeReg, Mode: 0o400,
		Uid: owner, Gid: owner, Size: int64(len(key)),
	}
	if err := tw.WriteHeader(header); err != nil {
		return nil, err
	}
	if _, err := tw.Write([]byte(key)); err != nil {
		return nil, err
	}
	if err := tw.Close(); err != nil {
		return nil, err
	}
	return &archive, nil
}

// keyFileOwner returns the numeric user mongod runs as, from the user the
// image declares rather than from the image's name.
//
//	""          999    the official image runs as root and drops to mongodb
//	"1001:0"    1001   a number is used as given
//	"mongod"    1000   a name is assumed to be one of MongoDB's own builds
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
