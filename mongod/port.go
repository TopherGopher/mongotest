package mongod

import (
	"net"
)

// GetAvailablePort returns a TCP port that is free right now, by asking the
// kernel for one and immediately giving it back.
//
// It exists for callers who need a fixed port and pass it to WithPort. It is
// not used on the default path, and it cannot be: between this call returning
// and a container binding the port, something else may take it. Letting the
// daemon assign the port and reading it back from inspect has no such window,
// which is what Start does when WithPort is not given.
func GetAvailablePort() (int, error) {
	ln, err := net.Listen("tcp", net.JoinHostPort(loopback, "0"))
	if err != nil {
		return 0, &portLookupError{Err: err}
	}
	defer func() { _ = ln.Close() }()

	addr, ok := ln.Addr().(*net.TCPAddr)
	if !ok {
		return 0, &portLookupError{Err: errNotATCPAddress}
	}
	return addr.Port, nil
}
