package mongod

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/tophergopher/mongotest/dockerclient"
	"github.com/tophergopher/mongotest/internal/reaper"
)

// Container is a running mongod, created by Start and removed by Stop. It
// holds no MongoDB client: hand URI to a driver to connect.
//
// Every method is safe to call from several goroutines.
type Container struct {
	id         string
	name       string
	image      MongoImage
	replicaSet string
	username   string
	password   string
	tls        bool
	host       string
	port       int
	docker     dockerclient.Client
	logger     dockerclient.Logger

	// mu guards stopped and reaperHandle. Stop can be called from a defer, a
	// test cleanup and the reaper at once, and holds mu while it waits for
	// the daemon; see lockContext.
	mu           sync.Mutex
	stopped      bool
	reaperHandle reaper.Handle
}

// Format prints the container as its name and URI with the password hidden,
// whatever the verb, so that logging a Container cannot leak its credentials.
func (c *Container) Format(f fmt.State, _ rune) {
	if c == nil {
		fmt.Fprint(f, "<nil>")
		return
	}
	fmt.Fprintf(f, "%s (%s)", c.name, c.uri(redactedPassword))
}

// ID returns the container id the daemon assigned.
//
//	3f1a9c4b7e2d5a8f0b6c3e9d1a4f7b2c5e8d0a3f6b9c2e5d8a1f4b7c0e3d6a9f
func (c *Container) ID() string { return c.id }

// Name returns the container name: the one WithName gave, or a generated one.
//
//	mongotest-1a2b3c4d
func (c *Container) Name() string { return c.name }

// Image returns the reference of the image the container runs.
//
//	mongo:8
//	public.ecr.aws/docker/library/mongo:8.0
func (c *Container) Image() string { return c.image.Reference() }

// MongoImage returns the image in its three parts.
//
//	Registry:   "public.ecr.aws"
//	Repository: "docker/library/mongo"
//	Version:    "8.0"
func (c *Container) MongoImage() MongoImage { return c.image }

// ReplicaSet returns the name given to WithReplicaSet, or "".
//
//	rs0
//
// Atlas Local names its own replica set, so ReplicaSet returns "" for it
// too. See [MongoImage.ReplicaSetPreconfigured].
func (c *Container) ReplicaSet() string { return c.replicaSet }

// Username returns mongod's root user, or "" if it does not authenticate.
//
//	admin       // WithAuth("admin", "secret")
//	mongotest   // generated; see WithAuth
func (c *Container) Username() string { return c.username }

// Password returns the root user's password, or "" if there is none. URI
// already includes it.
func (c *Container) Password() string { return c.password }

// Port returns the host port mongod is published on.
//
//	32768
func (c *Container) Port() int { return c.port }

// Host returns the address mongod is reachable at from this process. It is
// not always loopback; see [WithHostIP].
//
//	127.0.0.1    // local daemon
//	172.17.0.1   // this process is in a sibling container
//	build-host   // DOCKER_HOST=tcp://build-host:2376
func (c *Container) Host() string { return c.host }

// URI returns the connection string to hand to a driver. It includes the
// password when there is one, so keep it out of logs.
//
//	mongodb://127.0.0.1:32768/?directConnection=true
//	mongodb://admin:secret@127.0.0.1:32768/?directConnection=true   // WithAuth
//	mongodb://127.0.0.1:32768/?directConnection=true&tls=true       // WithTLS
//
// directConnection makes a driver use this address. Without it a driver
// switches to the address a replica set member advertises, which only works
// inside the container.
func (c *Container) URI() string { return c.uri(c.password) }

// redactedPassword replaces the password in a URI written for a person.
const redactedPassword = "xxxxx"

// uri builds the connection string around the given password, so the logged
// form and the real one cannot drift apart.
func (c *Container) uri(password string) string {
	var b strings.Builder
	b.WriteString("mongodb://")
	if c.username != "" {
		b.WriteString(escapeUserinfo(c.username) + ":" + escapeUserinfo(password) + "@")
	}
	b.WriteString(addr(c.host, c.Port()))
	b.WriteString("/?directConnection=true")
	if c.tls {
		b.WriteString("&tls=true")
	}
	return b.String()
}

// unreserved are the characters a connection string never needs escaped.
const unreserved = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789-._~"

// escapeUserinfo percent-encodes a username or password for a connection
// string. net/url is not strict enough: it leaves "$" and "+" alone, and
// drivers disagree about what those mean.
func escapeUserinfo(s string) string {
	const hex = "0123456789ABCDEF"
	var b strings.Builder
	for i := range len(s) {
		c := s[i]
		if strings.IndexByte(unreserved, c) >= 0 {
			b.WriteByte(c)
			continue
		}
		b.WriteByte('%')
		b.WriteByte(hex[c>>4])
		b.WriteByte(hex[c&0x0f])
	}
	return b.String()
}

// Stop removes the container and its anonymous volumes. It is safe to call
// twice and on a nil Container, and a container that is already gone counts
// as stopped. If ctx ends or the daemon refuses, Stop returns the error and
// can be called again.
func (c *Container) Stop(ctx context.Context) error {
	if c == nil {
		return nil
	}
	if err := lockContext(ctx, &c.mu); err != nil {
		return err
	}
	defer c.mu.Unlock()
	if c.stopped {
		return nil
	}

	err := c.docker.ContainerRemove(ctx, c.id, dockerclient.RemoveOptions{Force: true, RemoveVolumes: true})
	if err != nil && !dockerclient.IsNotFound(err) {
		return err
	}
	c.stopped = true
	reaper.Unregister(c.reaperHandle)
	c.logger.Debug("container removed", "container", c.name, "id", shortID(c.id))
	return nil
}

// lockPoll is how often lockContext tries again.
const lockPoll = 5 * time.Millisecond

// lockContext locks mu, or gives up when ctx ends. Stop holds its lock
// across a call to the daemon, so a second Stop, such as the reaper's on a
// signal, has to be able to stop waiting when its deadline passes.
func lockContext(ctx context.Context, mu *sync.Mutex) error {
	for !mu.TryLock() {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(lockPoll):
		}
	}
	return nil
}
