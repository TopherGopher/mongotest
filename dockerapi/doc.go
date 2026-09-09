// Package dockerapi is a minimal Docker Engine API client built on the Go
// standard library only.
//
// It exists so that mongotest can start, inspect, exec into and remove
// containers without depending on github.com/docker/docker or its
// successors. Only the handful of endpoints mongotest needs are implemented.
//
// A Client is created with New or FromEnv. Without an explicit WithHost the
// daemon is located the same way the docker CLI does it: the DOCKER_HOST
// environment variable, then the DOCKER_CONTEXT variable or the
// currentContext recorded in the Docker config directory, then the default
// unix socket /var/run/docker.sock. Supported host forms are
// unix:///path/to/docker.sock, tcp://host:port, http://host:port and
// https://host:port. Windows named pipes (npipe://) are not supported; point
// DOCKER_HOST at a TCP endpoint instead.
//
// TLS for tcp hosts follows the docker CLI conventions: DOCKER_TLS_VERIFY
// enables it and DOCKER_CERT_PATH (default: the config directory) holds
// ca.pem and, optionally, cert.pem and key.pem.
package dockerapi
