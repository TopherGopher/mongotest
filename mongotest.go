// Package mongotest provides helpers for running regressions using mongo.
// You can find helpers for:
// - running a database using docker
// - TODO: importing data to the DB from files
// - TODO: exporting data from the DB to a file
// - TODO: cleaning up a database
package mongotest

import (
	"archive/tar"
	"bytes"
	"context"
	"errors"
	"fmt"
	"github.com/docker/docker/api/types"
	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/mount"
	"github.com/docker/docker/api/types/network"
	"github.com/docker/go-connections/nat"
	docker "github.com/moby/docker/client"
	v1 "github.com/opencontainers/image-spec/specs-go/v1"
	"github.com/sirupsen/logrus"
	"github.com/tophergopher/easymongo"
	"go.mongodb.org/mongo-driver/mongo"
	"io"
	"io/ioutil"
	"math/rand"
	"net"
	"os"
	"runtime"
	"runtime/debug"
	"strconv"
	"time"
)

// TestConnection contains helpers for creating your own tests with mongo.
// Each TestConnection corresponds 1-to-1 with a docker container.
// Each docker container is hosted on a unique port.
type TestConnection struct {
	*easymongo.Connection
	dockerClient     *docker.Client
	logger           *logrus.Entry
	mongoContainerID string
	caPemFile        *os.File
	portNumber       int
	mongoURI         string
	mongoVersion     string
}

// initDocker initializes the various docker components we need
// It must be called before interacting with any docker components
func (testConn *TestConnection) initDocker() error {
	dockerClient, err := docker.NewEnvClient()
	if err != nil {
		testConn.logger.WithField("err", err).Error("Could not connect to docker daemon")
		return ErrFailedToConnectToDockerDaemon
	}
	testConn.dockerClient = dockerClient
	return nil
}

// spawnAndStartMongoContainer finds an available port and launches a mongo server docker container.
// It returns the mongoURI, the port the mongo service is hosted on.
// This must be called after initDocker.
func (testConn *TestConnection) spawnAndStartMongoContainer(initTLS bool, replicaSetName *string) (err error) {
	testConn.portNumber, err = GetAvailablePort()
	if err != nil {
		testConn.logger.WithField("err", err).Error("No ports were available to bind the test docker mongo container to")
		return ErrNoAvailablePorts
	}
	// TODO: Consider using different error types for these returns
	testConn.mongoContainerID, err = testConn.startMongoContainer(testConn.mongoVersion, testConn.portNumber, initTLS, replicaSetName)
	if err != nil {
		testConn.logger.WithField("err", err).Error("Could not spawn the to mongo container")
		return err
	}
	testConn.mongoURI = fmt.Sprintf("mongodb://127.0.0.1:%d", testConn.portNumber)
	if replicaSetName != nil {
		testConn.mongoURI += "/?replicaSet=" + *replicaSetName
	}
	// TODO: Add container to a global connection pool - ensure the connection pool
	// is reaped when existing unless DisableContainerReaping is enabled.
	return nil
}

// NewReplicaSetContainer spawns a new docker container and configures it as a 1 member
// replicaset. The resulting connection is returned.
func NewReplicaSetContainer(rsName string) (*TestConnection, error) {
	conn, err := initTestConnectionAndContainer(true, &rsName)
	if err != nil {
		return conn, err
	}

	return conn, nil
}

// NewTestConnection is the standard method for initializing a TestConnection - it has a side-effect
// of spawning a new docker container if spinupDockerContainer is set to true.
// Note that the first time this is called on a new system, the mongo docker
// container will be pulled. Any subsequent calls on the system should succeed without
// calls to pull.
// If spinupDockerContainer is False, then no docker shenanigans occur, instead
// an attempt is made to connect to a locally running mongo instance
// (e.g. mongodb://127.0.0.1:27017).
func NewTestConnection(spinupDockerContainer bool) (*TestConnection, error) {
	return initTestConnectionAndContainer(spinupDockerContainer, nil)
}

// initTestConnectionAndContainer does all the juicy logic of actually creating a docker client,
// spawning the mongo container, connecting to the mongo container and optionally initializing a replicaSet.
func initTestConnectionAndContainer(spinupDockerContainer bool, replicaSetName *string) (*TestConnection, error) {
	// TODO: How should we be handling logging? What do other libraries typically do?
	logger := logrus.New().WithField("src", "mongotest.TestConnection")
	testConn := &TestConnection{
		logger:       logger,
		mongoVersion: "latest",
	}
	defer func() {
		if err := recover(); err != nil {
			logger.WithFields(logrus.Fields{
				"err":   err,
				"stack": string(debug.Stack()),
			}).Error("A panic occurred when trying to initialize a TestConnection - auto-destroying mongo container")
			// Initialization crashed - ensure the mongo container is destroyed
			_ = testConn.KillMongoContainer()
			// Re-raise the panic
			panic(err)
		}
	}()
	if err := testConn.initDocker(); err != nil {
		logger.WithFields(logrus.Fields{
			"err":      err,
			"mongoURI": testConn.mongoURI,
		}).Error("Could not init the docker client - is the docker damon running?")
		return testConn, err
	}
	if spinupDockerContainer {
		initTLS := false
		err := testConn.spawnAndStartMongoContainer(initTLS, replicaSetName)
		if err != nil {
			// Error logged already
			return testConn, err
		}
		// Try using a finalizer to kill the mongo container if it goes out of scope
		// A note that finalizers are not guaranteed to run, they help when the
		runtime.SetFinalizer(testConn, func(tc *TestConnection) {
			_ = tc.KillMongoContainer()
		})
		// Cache the connection to allow for auto-reaping later
		cacheConnection(testConn)
	}
	if replicaSetName != nil {
		// Set up the replicaset prior to connecting
		mongoRsInitScript := fmt.Sprintf("rs.initiate({'_id': 'cf', 'members': [{'_id': 0, 'host': 'localhost:27017'}]})")
		numRetries := 3
		var output string
		var err error
		for i := 1; i <= numRetries; i++ {
			output, err = testConn.RunMongoScriptOnContainer(mongoRsInitScript)
			if err == nil {
				// If it came back successfully, break out of the retry loop
				break
			}
			// Otherwise, wait a tick and try again - note that each retry waits a little longer - just a
			// basic linear back-off.
			time.Sleep(time.Millisecond * 200 * time.Duration(numRetries))
			// If we are at the end of our retries, err will be preserved outside the loop
		}
		if err != nil {
			logger.WithFields(logrus.Fields{
				"err":      err,
				"mongoURI": testConn.mongoURI,
				"output":   output,
			}).Error("Could not make container into a replicaset after multiple retries")
			return testConn, err
		}
	}
	conn, err := easymongo.ConnectWith(testConn.mongoURI).Connect()
	testConn.Connection = conn
	// also create a quick-fail connection for the ping
	if err != nil {
		logger.WithFields(logrus.Fields{
			"err":      err,
			"mongoURI": testConn.mongoURI,
		}).Error("Could not connect to mongo instance")
		return testConn, err
	}
	// Allow up to 1 second for the mongo container to come up across 5 retrie=
	numChecks := 5
	sleepTime := time.Millisecond * 200
	for i := 0; i < numChecks; i++ {
		// var servErr mongo.ServerError
		// var cmdErr mongo.CommandError
		if err = conn.Ping(); err == nil {
			// If we were able to ping the instance, we can break
			break
		}
		// else if isServError := errors.As(err, &servErr); isServError {
		// 	fmt.Println(servErr)
		// } else if errors.As(err, &cmdErr) {
		//     fmt.Println(cmdErr)
		// } else if errors.Is(err, topology.ErrServerSelectionTimeout) {
		// 	fmt.Println("SERVER SELECTION TIMEOUT ERROR")
		// }
		logger.WithFields(logrus.Fields{
			"currentRetry":      i + 1,
			"maxRetries":        numChecks,
			"sleepMilliseconds": sleepTime.Milliseconds(),
		}).Debug("Could not connect to test database - sleeping and retrying.")
		// otherwise, we need to wait a bit before checking again
		time.Sleep(sleepTime)
	}
	if err != nil {
		logger.WithFields(logrus.Fields{
			"err":      err,
			"mongoURI": testConn.mongoURI,
		}).Errorf("Could not ping the test mongo instance after %d checks", numChecks)
		// Try to teardown the mongo container (it might not have started)
		_ = testConn.KillMongoContainer()
		return testConn, err
	}
	// The container is now alive and mongo is responding to pings
	return testConn, nil
}

// MongoContainerID returns the ID of the running docker container
// If no container is running, an empty string will be returned.
func (tc *TestConnection) MongoContainerID() string {
	return tc.mongoContainerID
}

// func (tc *TestConnection) ImportFromFile(filepath string) {
// 	// Open the file

// }

// GetAvailablePort returns an available port on the system.
func GetAvailablePort() (port int, err error) {
	// Create a new server without specifying a port
	// which will result in an open port being chosen
	server, err := net.Listen("tcp", "127.0.0.1:0")
	// If there's an error it likely means no ports
	// are available or something else prevented finding
	// an open port
	if err != nil {
		return 0, ErrNoAvailablePorts
	}
	defer server.Close()
	// Get the host string in the format "127.0.0.1:4444"
	hostString := server.Addr().String()
	// Split the host from the port
	_, portString, err := net.SplitHostPort(hostString)
	if err != nil {
		return 0, err
	}

	// Return the port as an int
	// TODO: This is used as a string elsewhere - consider string
	return strconv.Atoi(portString)
}

// pullMongoContainer fetches the mongo container from dockerhub
func (tc *TestConnection) pullMongoContainer(mongoImageName string) (err error) {
	// TODO: Is this better to do as an error handler?
	// Pull the initial container
	tc.logger.Info("Starting mongo docker image pull")
	rc, err := tc.dockerClient.ImagePull(context.Background(), mongoImageName, types.ImagePullOptions{})
	if err != nil {
		return fmt.Errorf("could not pull mongo container: %v", err)
	}
	defer rc.Close()
	if _, err := ioutil.ReadAll(rc); err != nil {
		return fmt.Errorf("could not pull mongo container: %v", err)
	}
	tc.logger.Info("Done pulling mongo docker image")
	return nil
}

func containerConfig(mongoImageName, portName string, useTLS bool, replicaSetName *string) *container.Config {
	conf := &container.Config{
		Image: mongoImageName,
		Labels: map[string]string{
			"mongotest": "regression",
		},
		Tty:       true,
		OpenStdin: true,
		ExposedPorts: nat.PortSet{
			nat.Port(portName): {},
		},
		Cmd: []string{},
	}
	if useTLS {
		// These flags are based on this docker run command:
		// docker run -d -v /path/to/pem/:/etc/ssl/ mongo:3.6 --sslMode requireSSL --sslPEMKeyFile /etc/ssl/mongodb.pem <additional options>
		// TODO: Figure out how to mount a volume properly - may be right without this map
		// -v /path/to/pem/:/etc/ssl/
		// conf.Volumes = map[string]struct{}{}
		conf.Cmd = []string{"--sslMode", "requireSSL", "--sslPEMKeyFile", "/etc/ssl/mongodb.pem"}
	}
	if replicaSetName != nil {
		conf.Cmd = append(conf.Cmd, "--replSet", *replicaSetName)
	}
	return conf
}

// These flags are based on this docker run command:
// docker run -d -v /path/to/pem/:/etc/ssl/ mongo:3.6 --sslMode requireSSL --sslPEMKeyFile /etc/ssl/mongodb.pem <additional options>
func dockerHostConfigWithTLS(portName string) (conf *container.HostConfig, caPemFile *os.File) {
	// Get the default dockerHostConfig
	conf = dockerHostConfig(portName)
	// Write out
	caPemFile, err := ioutil.TempFile(os.TempDir(), "mongo-tls-")
	if err != nil {
		panic(fmt.Errorf("could not create temporary file during testing: %w", err))
	}
	_, pemFile, _ := GenerateCARoot()
	_, err = caPemFile.Write(pemFile)
	if err != nil {
		panic(fmt.Errorf("could not write cert to temporary file during testing: %w", err))
	}
	conf.Mounts = []mount.Mount{{
		Type: mount.TypeBind,
		// Source is the host path - point at the CA cert that was just generated
		Source: caPemFile.Name(),
		// Target is the path inside docker - technically the recommended command mounts
		// the whole directory, but this should work
		Target: "/etc/ssl/mongodb.pem",
	}}
	return conf, caPemFile
}

func dockerHostConfig(portName string) *container.HostConfig {
	conf := &container.HostConfig{
		PortBindings: nat.PortMap{
			nat.Port("27017/tcp"): []nat.PortBinding{
				{
					HostIP:   "127.0.0.1",
					HostPort: portName,
				},
			},
		},
	}

	return conf
}

// startMongoContainer starts a mongo docker container
// A note that the docker daemon on the system is expected to be running
// TODO: Is there a way to spawn the docker daemon myself?
func (tc *TestConnection) startMongoContainer(mongoVersion string, portNumber int, initTLS bool, replicaSetName *string) (containerID string, err error) {
	if len(tc.mongoContainerID) != 0 {
		return "", ErrMongoContainerAlreadyRunning
	}
	portName := fmt.Sprintf("%d/tcp", portNumber)
	containerName := fmt.Sprintf("mongo-%d", portNumber)
	mongoImageName := "registry.hub.docker.com/library/mongo:" + mongoVersion
	hostConf := dockerHostConfig(portName)
	if initTLS {
		hostConf, tc.caPemFile = dockerHostConfigWithTLS(portName)
	}
	containerResp, err := tc.dockerClient.ContainerCreate(
		context.Background(),
		containerConfig(mongoImageName, portName, initTLS, replicaSetName),
		hostConf,
		&network.NetworkingConfig{},
		&v1.Platform{
			Architecture: "amd64",
			OS:           "linux",
		},
		containerName)
	if err != nil && docker.IsErrNotFound(err) {
		// The image didn't exist locally - go grab it
		if err = tc.pullMongoContainer(mongoImageName); err != nil {
			// The pull didn't succeed, bail
			tc.logger.WithField("err", err).Error("Could not pull the docker container")
			return "", err
		}
		// Now that the pull is complete, we can try to call start again
		return tc.startMongoContainer(mongoVersion, portNumber, initTLS, replicaSetName)
	} else if err != nil {
		tc.logger.WithField("err", err).Error("Could not start the docker container")
		return "", err
	}
	containerID = containerResp.ID
	tc.mongoContainerID = containerID

	err = tc.dockerClient.ContainerStart(
		context.Background(),
		containerID,
		types.ContainerStartOptions{})
	if err != nil {
		tc.logger.WithFields(logrus.Fields{
			"containerID": containerID,
			"err":         err,
		}).Error("Could not start the docker container")
		return containerID, err
	}
	tc.logger.WithFields(
		logrus.Fields{
			"containerName":      containerName,
			"containerMongoPort": portNumber,
			"containerID":        containerID,
		},
	).Info("Successfully spawned mongo docker test container.")
	return containerID, err
}

// RunMongoScriptOnContainer takes a string representing a mongo JS script. This can have
// new lines. This must
func (tc *TestConnection) RunMongoScriptOnContainer(mongoScript string) (output string, err error) {
	// Create a destination path within the container which is reasonably* unique
	fname := fmt.Sprintf("mongoScript-%s.js", strconv.Itoa(rand.Intn(9999999)))
	folderName := "/tmp/"
	destinationPath := folderName + fname
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	header := &tar.Header{
		Name: fname,
		Mode: 0777,
		Size: int64(len(mongoScript)),
	}
	if err = tw.WriteHeader(header); err != nil {
		return output, fmt.Errorf("could not write tar header to archive when copying file to mongo: %w", err)
	}

	if _, err = tw.Write([]byte(mongoScript)); err != nil {
		return output, fmt.Errorf("could not copy mongo script into a tar archive in preparation for copying to container: %w", err)
	}
	_ = tw.Flush()
	if err = tw.Close(); err != nil {
		return output, fmt.Errorf("could not close tar archive in preparation for copying to container: %w", err)
	}
	if err = tc.dockerClient.CopyToContainer(context.Background(), tc.mongoContainerID,
		folderName, &buf, types.CopyToContainerOptions{}); err != nil {
		return output, fmt.Errorf("could not copy file from host to container: %w", err)
	}

	// and execute the file
	cmd := []string{"mongo", destinationPath}
	output, err = tc.ExecCommandInMongoContainer(cmd)
	return output, err
}

// ExecCommandInMongoContainer attaches to the mongo container and executes the provided command
// In the case that an error occurs either spawning the docker context or executing the command, an error
// will be returned. In the case that an error is returned from a malformed/bad command, then output
// is also populated. It is recommended not to use mongo --eval here as the script does not
// seem to reliably run. Instead, it's recommended to use
func (tc *TestConnection) ExecCommandInMongoContainer(cmd []string) (output string, err error) {
	execOpts := types.ExecConfig{
		Privileged:   false,
		Tty:          true,
		AttachStdin:  true,
		AttachStderr: true,
		AttachStdout: true,
		Cmd:          cmd,
	}
	execIDObj, err := tc.dockerClient.ContainerExecCreate(context.Background(), tc.mongoContainerID, execOpts)
	if err != nil {
		err = fmt.Errorf("could not create execution context for container %s: %w", tc.mongoContainerID, err)
		tc.logger.WithFields(logrus.Fields{
			"err": err,
			"cmd": cmd,
		}).Error("Could not create execution context for provided command")
		return output, err
	}
	// Kick off the command and attach to the container - it will return a reader object we can read from
	attachedRes, err := tc.dockerClient.ContainerExecAttach(context.Background(), execIDObj.ID, types.ExecStartCheck{
		Detach: false,
		Tty:    true,
	})
	if err != nil {
		// Error attaching to container - we won't get an
		err = fmt.Errorf("could not attach to execution context for container %s: %w", tc.mongoContainerID, err)
		tc.logger.WithFields(logrus.Fields{
			"err": err,
			"cmd": cmd,
		}).Error("Could not attach to execution context for provided container")
		return output, err
	}
	defer attachedRes.Close()
	resultsExist := true
	msg := fmt.Sprintf("Executing in container %s - \n\t", tc.mongoContainerID)
	for _, c := range cmd {
		msg += c + " "
	}
	output += msg + "\n"
	output += "--------------------\n"
	results := ""
	for resultsExist {
		// Walk through the results - could be successful or not
		line, _, err := attachedRes.Reader.ReadLine()
		if err != nil {
			if errors.Is(err, io.EOF) {
				// No more results from command - break out of the loop
				resultsExist = false
				break
			}
			err = fmt.Errorf("could not read lines from container '%s': %w", tc.mongoContainerID, err)
			tc.logger.WithFields(logrus.Fields{
				"err": err,
				"cmd": cmd,
			}).Error("Could not execute provided command")
			return output, err
		}
		results += string(line) + "\n"
	}
	output += results + "\n"
	output += "--------------------\n"

	inspectRes, err := tc.dockerClient.ContainerExecInspect(context.Background(), execIDObj.ID)
	if err != nil {
		err = fmt.Errorf("could not inspect command execution in container %s: %w", tc.mongoContainerID, err)
		tc.logger.WithFields(logrus.Fields{
			"err": err,
			"cmd": cmd,
		}).Error("Could not inspect container after executing provided command")
		return output, err
	}
	if inspectRes.ExitCode > 0 {
		// The command itself returned a bad exit code (e.g. malformed command)
		err = fmt.Errorf("could not execute provided command in container %s: \n%s", tc.mongoContainerID, results)
		tc.logger.WithFields(logrus.Fields{
			"err": err,
			"cmd": cmd,
		}).Debug("There was an error executing the provided command")
	}
	return output, err
}

// KillMongoContainer tears down the specified container
// This is called as part of a finalizer automatically. There is no guarantee that
// the finalizer will run prior to a program exiting, but a best attempt has been made
func (tc *TestConnection) KillMongoContainer() (err error) {
	if tc == nil {
		return nil
	}
	if len(tc.mongoContainerID) == 0 {
		// No container was ever launched, nothing to be done
		return nil
	}
	if tc.caPemFile != nil {
		// If a tmp CA pem file was written out to the OS, attempt to clean it up
		err = os.Remove(tc.caPemFile.Name())
		tc.logger.WithFields(logrus.Fields{
			"err":         err,
			"containerID": tc.caPemFile.Name(),
		}).Error("Could not delete generated CA PEM temporary file - still will attempt to teardown docker container...")
		err = nil
		tc.caPemFile = nil
	} // Note that we do not error out if we couldn't clean-up the temporary file
	err = tc.dockerClient.ContainerRemove(context.Background(),
		tc.mongoContainerID,
		types.ContainerRemoveOptions{
			RemoveVolumes: true,
			Force:         true,
		})
	if err != nil {
		tc.logger.WithFields(logrus.Fields{
			"err":         err,
			"containerID": tc.mongoContainerID,
		}).Error("Could not remove container")
		return err
	}
	tc.logger.WithField("containerID", tc.mongoContainerID).Debug(
		"Successfully removed container")
	// Once removed - unset the container ID
	tc.mongoContainerID = ""
	return nil
}

// EasyMongoWithContainer spawns a docker container on an available port,
// connects to the mongo database, runs the provided function,
// then kills the mongo container as it exits.
// A note that the function isn't actually executed inside the container, instead
// a connection is established to the mongo server from the host system.
func EasyMongoWithContainer(f func(c *easymongo.Connection) error) (err error) {
	spinupDockerContainer := true
	tc, err := NewTestConnection(spinupDockerContainer)
	if err != nil {
		return err
	}
	defer tc.KillMongoContainer()
	// Run whatever function it is
	return f(tc.Connection)
}

// MongoClientWithContainer spawns a docker container on an available port,
// connects to the mongo database, runs the provided function,
// then kills the mongo container as it exits.
// A note that the function isn't actually executed inside the container, instead
// a connection is established to the mongo server from the host system.
func MongoClientWithContainer(f func(m *mongo.Client) error) error {
	spinupDockerContainer := true
	tc, err := NewTestConnection(spinupDockerContainer)
	if err != nil {
		return err
	}
	defer tc.KillMongoContainer()
	// Run whatever function it is using the mongo driver connection
	return f(tc.Connection.MongoDriverClient())
}

// TODO: DropAllDatabases
// TODO
