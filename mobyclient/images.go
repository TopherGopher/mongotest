package mobyclient

import (
	"context"
	"errors"
	"net/http"

	"github.com/moby/moby/client"

	"github.com/tophergopher/mongotest/dockerclient"
)

// ImagePull fetches ref from its registry. It returns once the daemon has
// finished, and reports a failure the daemon sends inside the pull progress
// stream as a *dockerclient.PullError.
//
// The moby client normalizes ref before pulling (a bare repository name
// such as "mongo:8" becomes "docker.io/library/mongo:8" on the wire), the
// same way the docker CLI and a real daemon do. ImageInspect and
// ContainerCreate send whatever reference they are given unchanged, so
// against a real daemon "mongo:8" still finds the image the pull produced:
// the daemon applies the same normalization on its side when matching.
func (c *Client) ImagePull(ctx context.Context, ref string) error {
	if _, err := dockerclient.ParseImageRef(ref); err != nil {
		return err
	}
	resp, err := c.cli.ImagePull(ctx, ref, client.ImagePullOptions{})
	if err != nil {
		return c.mapErr(http.MethodPost, "/images/create", err)
	}
	defer resp.Close()
	if err := resp.Wait(ctx); err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return err
		}
		return &dockerclient.PullError{Ref: ref, Message: err.Error(), Err: err}
	}
	return nil
}

// ImageInspect returns metadata for a local image, by reference ("mongo:8")
// or id ("sha256:..."). A missing image is ErrNotFound.
func (c *Client) ImageInspect(ctx context.Context, ref string) (dockerclient.ImageInspect, error) {
	ref, err := dockerclient.CheckImageRefOrID(ref)
	if err != nil {
		return dockerclient.ImageInspect{}, err
	}
	res, err := c.cli.ImageInspect(ctx, ref)
	if err != nil {
		return dockerclient.ImageInspect{}, c.mapErr(http.MethodGet, "/images/"+ref+"/json", err)
	}
	return dockerclient.ImageInspect{
		ID:           res.ID,
		RepoTags:     res.RepoTags,
		Architecture: res.Architecture,
		OS:           res.Os,
	}, nil
}
