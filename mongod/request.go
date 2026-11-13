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
func (r Resolved) containerConfig(host string) dockerclient.ContainerConfig {
	hostPort := "" // empty asks the daemon to choose
	if r.Port != 0 {
		hostPort = strconv.Itoa(r.Port)
	}
	return dockerclient.ContainerConfig{
		Image:        r.Image.Reference(),
		Cmd:          r.command(),
		Env:          r.credentialEnv(),
		Labels:       maps.Clone(r.Labels),
		ExposedPorts: map[string]struct{}{MongoPort: {}},
		HostConfig: &dockerclient.HostConfig{PortBindings: map[string][]dockerclient.PortBinding{
			MongoPort: {{HostIP: bindIP(host), HostPort: hostPort}},
		}},
	}
}

// command returns the mongod flags. The image's entrypoint supplies the
// program, except in an image that takes no flags, which gets no command.
func (r Resolved) command() []string {
	if !r.Image.AcceptsMongodArgs() {
		return nil
	}
	var cmd []string
	if r.ReplicaSet != "" {
		cmd = append(cmd, "--replSet", r.ReplicaSet)
	}
	if r.needsKeyFile() {
		cmd = append(cmd, "--keyFile", keyFilePath)
	}
	return append(cmd, r.MongodArgs...)
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
func (r Resolved) credentialEnv() []string {
	if r.Username == "" {
		return nil
	}
	var env []string
	for _, name := range rootCredentialEnv {
		env = append(env, name.username+"="+r.Username, name.password+"="+r.Password)
	}
	return env
}

// withCredentials generates a root user when the caller chose none and the
// port is about to be published where other machines can reach it.
func (r Resolved) withCredentials(host string) Resolved {
	if !r.needsGeneratedCredentials(host) {
		return r
	}
	r.Username, r.Password = DefaultUsername, randomHex(secretRandomBytes)
	r.Logger.Info("the port is published on every interface, so mongod is created with generated credentials; the container's URI carries them",
		"container", r.Name, "username", r.Username)
	return r
}

// needsGeneratedCredentials reports whether the caller left the choice open
// and the publish is wide enough to need one made.
func (r Resolved) needsGeneratedCredentials(host string) bool {
	chosen := r.NoAuth || r.Username != ""
	return !chosen && bindIP(host) == allInterfaces
}

// needsKeyFile reports whether mongod must be given a key file: a replica
// set member that authenticates will not start without one. An image that
// runs its own replica set brings its own.
func (r Resolved) needsKeyFile() bool {
	authenticates := r.Username != ""
	return authenticates && r.ReplicaSet != "" && r.Image.AcceptsMongodArgs()
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
// 127.0.1.1 would be refused. A name such as localhost is left for the
// dialler to resolve.
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
