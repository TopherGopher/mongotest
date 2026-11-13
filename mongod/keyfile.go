package mongod

import (
	"archive/tar"
	"bytes"
	"context"

	"github.com/tophergopher/mongotest/dockerclient"
)

// Where the replica set key is put inside the container. /etc exists in every
// image, and nothing else in one is called this.
const (
	keyFileDir  = "/etc"
	keyFileName = "mongotest-keyfile"
	keyFilePath = keyFileDir + "/" + keyFileName
)

// copyKeyFile puts a fresh replica set key into a container that has been
// created and not yet started, for mongod to be pointed at with --keyFile.
//
// mongod is particular about the file: it refuses one that group or other can
// read, so the file has to belong to the user mongod runs as, and that user is
// not root. The archive therefore carries the owner itself rather than going
// through CopyToContainer, which writes everything as root. A key owned by the
// wrong user ends the container with "error opening file ... bad file" in its
// log, which is what an image this package does not know produces if its
// mongod runs as someone else; see MongoImage.mongodUID.
//
// The key is used by nothing but this one member, so it is generated here and
// kept nowhere.
func copyKeyFile(ctx context.Context, docker dockerclient.Client, id string, image MongoImage) error {
	key := randomHex(secretRandomBytes)
	var archive bytes.Buffer
	tw := tar.NewWriter(&archive)
	owner := image.mongodUID()
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
