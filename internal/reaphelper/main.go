// Command reaphelper starts a MongoDB container and waits to be signalled.
// The reaper's integration test runs it to check that a signalled process
// removes its container and still dies of the signal, which only a parent
// process can observe.
//
// It prints "container <id> <name>" once the container is up.
package main

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/tophergopher/mongotest/mongod"
)

func main() {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	// The test labels the container so it can find one that leaks.
	c, err := mongod.Start(ctx, mongod.WithLabel("mongotest.reaphelper", os.Getenv("REAPHELPER_LABEL")))
	if err != nil {
		fmt.Fprintln(os.Stderr, "reaphelper: cannot start a container:", err)
		os.Exit(2)
	}
	fmt.Printf("container %s %s\n", c.ID(), c.Name())

	// Getting past this means no signal arrived, which is a failure.
	time.Sleep(5 * time.Minute)
	fmt.Fprintln(os.Stderr, "reaphelper: no signal arrived")
	os.Exit(3)
}
