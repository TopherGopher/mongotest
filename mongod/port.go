package mongod

import (
	"net"
)

// GetAvailablePort returns a TCP port that is free right now, for a caller
// who has to pass a fixed port to WithPort. Something else can take the port
// before a container binds it, which is why Start does not use this itself.
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
