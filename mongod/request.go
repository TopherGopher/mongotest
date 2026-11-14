package mongod

import (
	"maps"
	"net"
	"strconv"

	"github.com/tophergopher/mongotest/dockerclient"
)

// The two addresses a port is published on.
const (
	// loopback is reachable only from the daemon's own host.
	loopback = "127.0.0.1"
	// allInterfaces is reachable from anywhere that can reach that host.
	allInterfaces = "0.0.0.0"
)

// containerConfig builds the create request. host is the address the caller
// will dial, which decides the interface the port is published on.
func (c config) containerConfig(host string) dockerclient.ContainerConfig {
	publishOn := "" // empty asks the daemon to choose the port
	if c.port != 0 {
		publishOn = strconv.Itoa(c.port)
	}
	return dockerclient.ContainerConfig{
		Image:        c.image.Reference(),
		Cmd:          c.command(),
		Env:          c.credentialEnv(),
		Labels:       maps.Clone(c.labels),
		ExposedPorts: map[string]struct{}{mongoPort: {}},
		HostConfig: &dockerclient.HostConfig{PortBindings: map[string][]dockerclient.PortBinding{
			mongoPort: {{HostIP: bindIP(host), HostPort: publishOn}},
		}},
	}
}

// command returns the mongod flags. The image's entrypoint supplies the
// program, except in an image that takes no flags, which gets no command.
func (c config) command() []string {
	if !c.image.acceptsMongodArgs() {
		return nil
	}
	var cmd []string
	if c.replicaSet != "" {
		cmd = append(cmd, "--replSet", c.replicaSet)
	}
	if c.needsKeyFile() {
		cmd = append(cmd, "--keyFile", keyFilePath)
	}
	return append(cmd, c.mongodArgs...)
}

// rootCredentialEnv names the variables images read their root user from:
// the Docker official image the first pair, Atlas Local the second, and
// MongoDB's own builds either. Setting all four means nothing here has to
// know which image it is starting.
var rootCredentialEnv = [...]struct{ username, password string }{
	{"MONGO_INITDB_ROOT_USERNAME", "MONGO_INITDB_ROOT_PASSWORD"},
	{"MONGODB_INITDB_ROOT_USERNAME", "MONGODB_INITDB_ROOT_PASSWORD"},
}

// credentialEnv returns the environment that makes the image create the root
// user, or nil when there is none to create.
func (c config) credentialEnv() []string {
	if c.username == "" {
		return nil
	}
	var env []string
	for _, name := range rootCredentialEnv {
		env = append(env, name.username+"="+c.username, name.password+"="+c.password)
	}
	return env
}

// withCredentialsIfExposed generates a root user when the caller chose none
// and the port is about to be published where other machines can reach it.
func (c config) withCredentialsIfExposed(host string) config {
	if !c.needsGeneratedCredentials(host) {
		return c
	}
	c.username, c.password = DefaultUsername, randomHex(secretRandomBytes)
	c.logger.Info("the port is published on every interface, so mongod is created with generated credentials; the container's URI carries them",
		"container", c.name, "username", c.username)
	return c
}

// needsGeneratedCredentials reports whether the caller chose neither
// credentials nor WithoutAuth, and the port will be on every interface.
func (c config) needsGeneratedCredentials(host string) bool {
	chosen := c.noAuth || c.username != ""
	return !chosen && bindIP(host) == allInterfaces
}

// needsKeyFile reports whether mongod must be given a key file: a replica
// set member that authenticates will not start without one. An image that
// runs its own replica set brings its own.
func (c config) needsKeyFile() bool {
	authenticates := c.username != ""
	return authenticates && c.replicaSet != "" && c.image.acceptsMongodArgs()
}

// bindIP returns the interface to publish on for the address the caller will
// dial. Docker publishes on exactly the address it is given, so a port bound
// to loopback is refused from anywhere else.
//
// Loopback stays loopback, which keeps a laptop's mongod off the network. Any
// other address gets every interface, because the address a caller dials,
// such as a remote daemon's hostname, need not be one that host can bind.
func bindIP(host string) string {
	if isLoopback(host) {
		return loopback
	}
	return allInterfaces
}

// dialHost returns the address to dial for a resolved one. Every loopback IP
// becomes 127.0.0.1, the one address a loopback publish listens on; ::1 and
// 127.0.1.1 would be refused. A name such as localhost is left alone: a
// dialler tries each address it resolves to, so it finds 127.0.0.1 itself.
func dialHost(host string) string {
	if ip := net.ParseIP(host); ip != nil && ip.IsLoopback() {
		return loopback
	}
	return host
}

// isLoopback reports whether a host is this machine: localhost, or any
// loopback IP.
func isLoopback(host string) bool {
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}
