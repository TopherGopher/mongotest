package mongotest

import (
	"errors"
)

// Existing entrypoint - NewTestConnection(spinupDockerContainer bool) (*TestConnection, error)
// This entrypoint has disadvantages as it:
// - locks you into using easymongo (not a huge deal)
// - doesn't allow for TLS connectivity (but we could add a bool here)
// - doesn't allow a TLS data to be passed back up

// Let's design some new entrypoints
// TODO: Are there better names for these entrypoint functions?
// We want a normal entrypoint for those that don't care
//   - NewTestConnection(spinupDockerContainer bool) (*TestConnection, error)
// We want an entrypoint that allows a function to be run and the container and connection reaps itself
//   - EasyMongoWithContainer(f func(c *easymongo.Connection) error) (err error)
// And probably a similar one for people that don't want to adopt easymongo
//   - MongoClientWithContainer(f func(m *mongo.Client) error) error
// We want an entrypoint that configures mongo with TLS and returns the necessary configuration
//   -

// We need the ability to provide an optional port, TLS on/off, image version
// We need to get back the mongoURI, containerID
// If TLS is on, we also need some additional metadata (pem file, tmpFile handle) in order
// to finish configuring the DB connection.

var ErrNotImplemented = errors.New("function not yet implemented")

// // NewEasyMongoConnection spins up a new docker container
// // TODO: If we do it this way, you lose the ability to teardown the docker container
// func NewEasyMongoConnection() (*easymongo.Connection, error) {
// 	spinupDockerContainer := true
// 	tc, err := NewTestConnection(spinupDockerContainer)
// 	if err != nil {
// 		return nil, err
// 	}
// 	return tc.Connection, err
// }

// func NewMongoClientConnection() *mongo.Client {
// 	panic(ErrNotImplemented)
// 	return nil
// }

// func (tc *TestConnection) StartMongoTLSContainer(portNumber int, useSSL bool) {
// 	easymongo.ConnectWith("mongoURI").WithTLS()
// }
