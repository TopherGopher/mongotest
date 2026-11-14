package mongod_test

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"strconv"
	"strings"

	"github.com/tophergopher/mongotest/dockerclient"
	"github.com/tophergopher/mongotest/dockermock"
	"github.com/tophergopher/mongotest/mongod"
)

// stage stands in for Docker so the examples run anywhere. It is a fake
// daemon that says it is at 127.0.0.1 and publishes every container on a
// port where a stand-in for mongod answers. A real program passes no WithDocker,
// and Start finds the daemon itself.
type stage struct {
	*dockermock.Fake
	port int
}

func (s stage) Host() string { return "tcp://127.0.0.1:2375" }

func (s stage) ContainerInspect(ctx context.Context, id string) (dockerclient.ContainerInspect, error) {
	info, err := s.Fake.ContainerInspect(ctx, id)
	if err != nil {
		return info, err
	}
	info.NetworkSettings.Ports = map[string][]dockerclient.PortBinding{
		"27017/tcp": {{HostIP: "127.0.0.1", HostPort: strconv.Itoa(s.port)}},
	}
	return info, nil
}

// exampleDocker returns the stage, and a function that closes it.
func exampleDocker() (dockerclient.Client, func()) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		panic(err)
	}
	go acceptAll(listener, answerCommand)

	fake := dockermock.NewFake(dockermock.WithImages("mongo:8.0", "mongo:7.0", "mirror.corp.example/mongo:8.0"))
	fake.Processes = func(c dockermock.ContainerState) [][]string {
		if len(c.Config.Env) > 0 {
			return [][]string{{"1", "mongod --auth --bind_ip_all"}}
		}
		return [][]string{{"1", "mongod --bind_ip_all"}}
	}
	return stage{Fake: fake, port: listener.Addr().(*net.TCPAddr).Port}, func() { _ = listener.Close() }
}

func ExampleStart() {
	docker, closeDocker := exampleDocker()
	defer closeDocker()
	ctx := context.Background()

	c, err := mongod.Start(ctx, mongod.WithDocker(docker))
	if err != nil {
		fmt.Println("cannot start mongod:", err)
		return
	}
	defer c.Stop(ctx)

	// Hand c.URI() to a MongoDB driver.
	fmt.Println(c.Image())
	// Output: mongo:8.0
}

// A version on its own keeps the default image; a whole image can be one of
// the known ones, pointed at a mirror.
func ExampleWithImage() {
	docker, closeDocker := exampleDocker()
	defer closeDocker()
	ctx := context.Background()

	older, err := mongod.Start(ctx, mongod.WithDocker(docker), mongod.WithImage("7.0"))
	if err != nil {
		fmt.Println("cannot start mongod:", err)
		return
	}
	defer older.Stop(ctx)

	mirror := mongod.ImageDockerHub.WithRegistry("mirror.corp.example")
	mirrored, err := mongod.Start(ctx, mongod.WithDocker(docker), mongod.WithMongoImage(mirror))
	if err != nil {
		fmt.Println("cannot start mongod:", err)
		return
	}
	defer mirrored.Stop(ctx)

	fmt.Println(older.Image())
	fmt.Println(mirrored.Image())
	// Output:
	// mongo:7.0
	// mirror.corp.example/mongo:8.0
}

// MONGOTEST_IMAGE moves a whole suite to another image without editing a
// test, which is how CI gets off Docker Hub's pull limits.
func ExampleStart_environment() {
	docker, closeDocker := exampleDocker()
	defer closeDocker()
	ctx := context.Background()
	os.Setenv(mongod.EnvImage, "mongo:7.0")
	defer os.Unsetenv(mongod.EnvImage)

	c, err := mongod.Start(ctx, mongod.WithDocker(docker))
	if err != nil {
		fmt.Println("cannot start mongod:", err)
		return
	}
	defer c.Stop(ctx)

	fmt.Println(c.Image())
	// Output: mongo:7.0
}

func ExampleWithAuth() {
	docker, closeDocker := exampleDocker()
	defer closeDocker()
	ctx := context.Background()

	c, err := mongod.Start(ctx, mongod.WithDocker(docker), mongod.WithAuth("admin", "p@ssword"))
	if err != nil {
		fmt.Println("cannot start mongod:", err)
		return
	}
	defer c.Stop(ctx)

	// c.URI() carries the credentials, encoded, so it is all a driver needs.
	// Printing the container itself hides the password.
	fmt.Println(c.Username())
	fmt.Println("password printed:", strings.Contains(fmt.Sprint(c), "p@ssword"))
	// Output:
	// admin
	// password printed: false
}

// Every error matches a sentinel, so a caller never reads a message.
func ExampleStart_errors() {
	ctx := context.Background()

	_, err := mongod.Start(ctx, mongod.WithMongoImage(mongod.ImageCommunity.WithVersion("8")))
	fmt.Println("no such version:", errors.Is(err, mongod.ErrNoSuchVersion))

	_, err = mongod.Start(ctx, mongod.WithMongoImage(mongod.ImageAtlasLocal), mongod.WithMongodArgs("--quiet"))
	var unsupported *mongod.UnsupportedForImageError
	if errors.As(err, &unsupported) {
		fmt.Println("remove:", unsupported.Option)
	}
	// Output:
	// no such version: true
	// remove: WithMongodArgs
}

// Calling this after m.Run in a TestMain removes any container a test
// forgot to stop:
//
//	func TestMain(m *testing.M) {
//		code := m.Run()
//		if err := mongod.ReapRunningContainers(context.Background()); err != nil {
//			log.Print(err)
//		}
//		os.Exit(code)
//	}
func ExampleReapRunningContainers() {
	docker, closeDocker := exampleDocker()
	defer closeDocker()
	ctx := context.Background()

	if _, err := mongod.Start(ctx, mongod.WithDocker(docker)); err != nil {
		fmt.Println("cannot start mongod:", err)
		return
	}
	// Nothing stops the container.

	fmt.Println("reaped:", mongod.ReapRunningContainers(ctx) == nil)
	// Output: reaped: true
}
