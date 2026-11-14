package mongod

import (
	"context"
	"fmt"
	"strings"
	"time"
)

// initiateScript makes mongod the only member of its replica set, then waits
// until it is primary, which is when it accepts writes.
const initiateScript = `rs.initiate(); while (!db.hello().isWritablePrimary) sleep(50)`

// initiateReplicaSet runs initiateScript inside the container. Initiating
// needs a MongoDB client, and the image's own mongosh is the one that is
// already there.
func (c *Container) initiateReplicaSet(ctx context.Context, timeout time.Duration) error {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	c.logger.Debug("initiating the replica set", "container", c.name, "replicaSet", c.replicaSet)
	result, err := c.docker.Exec(ctx, c.id, c.mongosh(initiateScript)...)
	if err != nil {
		return c.startError(OpInitiate, fmt.Errorf("%w: %w", ErrNotInitiated, err))
	}
	if result.ExitCode != 0 {
		return c.startError(OpInitiate, fmt.Errorf("%w: mongosh exited with code %d: %s",
			ErrNotInitiated, result.ExitCode, strings.TrimSpace(result.Stderr)))
	}
	return nil
}

// mongosh returns the command that runs a script in the container's shell,
// as the root user when there is one.
func (c *Container) mongosh(script string) []string {
	cmd := []string{"mongosh", "--quiet", "--norc"}
	if c.username != "" {
		cmd = append(cmd, "--username", c.username, "--password", c.password, "--authenticationDatabase", "admin")
	}
	return append(cmd, "--eval", script)
}
