package mongod

import (
	"context"
	"strings"
	"sync"
	"time"

	"github.com/tophergopher/mongotest/dockerclient"
	"github.com/tophergopher/mongotest/reaper"
)

// Container is a running mongod, created by Start and removed by Stop. It
// holds no MongoDB client: hand URI to a driver to connect.
//
// Every method is safe to call from several goroutines.
type Container struct {
	id           string
	name         string
	image        MongoImage
	replicaSet   string
	username     string
	password     string
	tls          bool
	startTimeout time.Duration
	host         string
	// ports maps a container port such as "27017/tcp" to its host port.
	ports  map[string]int
	docker dockerclient.Client
	logger dockerclient.Logger

	// mu guards stopped and reaperHandle. Stop can be called from a defer, a
	// test cleanup and the reaper at once.
	mu           sync.Mutex
	stopped      bool
	reaperHandle reaper.Handle
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

// StartTimeout returns how long Start was allowed to wait for mongod.
//
//	1m0s
func (c *Container) StartTimeout() time.Duration { return c.startTimeout }

// ReplicaSet returns the name given to WithReplicaSet, or "".
//
//	rs0
//
// Atlas Local runs a replica set under a name of its own choosing, so it
// returns "" here too. See [MongoImage.ReplicaSetPreconfigured].
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
func (c *Container) Port() int { return c.ports[MongoPort] }

// Host returns the address mongod is reachable at from this process. It is
// not always loopback; see [Container.Endpoint].
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
// directConnection stops a driver from following a replica set member's own
// idea of its address, which is one inside the container.
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

// Endpoint returns the address to dial for a container port.
//
//	host, port, err := c.Endpoint("27017/tcp")   // "127.0.0.1", 32768, nil
//	_, _, err = c.Endpoint("28017/tcp")          // ErrNoPublishedPort
//
// The host is worked out once, when the container starts, from the first of
// these that applies:
//
//  1. WithHostIP;
//  2. the MONGOTEST_HOST_IP environment variable;
//  3. the daemon's own host, when the daemon is remote (tcp, http, https, ssh);
//  4. loopback, when this process shares the daemon host's network;
//  5. the default gateway, when this process is in a container with a network
//     of its own, because the port is published on the host outside it.
func (c *Container) Endpoint(containerPort string) (host string, port int, err error) {
	published, found := c.ports[containerPort]
	if !found {
		return "", 0, &UnpublishedPortError{Port: containerPort, Name: c.name, ID: c.id}
	}
	return c.host, published, nil
}

// Stop removes the container and its anonymous volumes. It is safe to call
// twice and on a nil Container, and a container that is already gone counts
// as stopped. If the daemon refuses, Stop can be called again.
func (c *Container) Stop(ctx context.Context) error {
	if c == nil {
		return nil
	}
	c.mu.Lock()
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
