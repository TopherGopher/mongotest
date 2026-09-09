// Package dockermock provides the test doubles for the Docker client
// interface, so that a project needs exactly one of them rather than a stub
// per package.
//
// Three doubles are offered, in increasing order of how much they do for
// you. All three satisfy dockerclient.Client, except Daemon, which sits one
// level lower.
//
// # Mock: assert the exact calls
//
// Mock has one function field per method and records every call. Leave a
// field nil and the method succeeds with a zero result, so a test only
// scripts the calls it cares about.
//
//	docker := &dockermock.Mock{
//		ContainerCreateFunc: func(ctx context.Context, name string, cfg dockerclient.ContainerConfig) (string, []string, error) {
//			return "c1", nil, nil
//		},
//	}
//	if err := StartMongo(ctx, docker); err != nil { ... }
//	// Then assert on what happened:
//	created := docker.CallsTo("ContainerCreate")
//
// Use it when the point of the test is which calls were made, in what order,
// with what arguments: that a missing image triggers exactly one pull and a
// retry, or that a failed start is followed by a remove.
//
// # Fake: a working daemon in memory
//
// Fake keeps an in-memory image and container store and behaves like a
// daemon: creating a container needs its image pulled first, starting one
// makes it running, inspecting it reports the ports it published, removing
// it makes later calls return ErrNotFound. Nothing is scripted.
//
//	docker := dockermock.NewFake()
//	// ImagePull, ContainerCreate, ContainerStart, ContainerInspect... all work.
//
// Use it when the test needs a container lifecycle to work so that the code
// under test can be exercised, and the Docker calls themselves are not the
// subject. Fake also applies the same validation as a real client, so a bad
// image reference or port binding fails in the test exactly as it would in
// production.
//
// # Daemon: a fake Docker daemon over HTTP
//
// Daemon is an httptest server that speaks the Engine API, so a real client
// (dockerapi or mobyclient) can be pointed at it with its WithHost option.
// It records every request, matches routes with {id} placeholders, and
// ignores the /v1.xx version prefix so tests work before and after version
// negotiation.
//
//	d := dockermock.NewDaemon()
//	defer d.Close()
//	d.ServeDefaults() // a daemon that answers every endpoint with fixed data
//	c, err := dockerapi.New(dockerapi.WithHost(d.Host()))
//
// Use it when the wire behaviour is the subject: request shape, status code
// handling, streaming, or the hijacked connection an exec uses. It is what
// the dockerapi tests and examples run against.
//
// # Choosing
//
//	Mock    the calls are the subject
//	Fake    the calls must work so something else can be tested
//	Daemon  the HTTP conversation is the subject
package dockermock
