package mongod

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"time"
)

// The parts of MongoDB's wire protocol a hello needs. Every message starts
// with four little-endian int32s: its length, its id, the id of the message
// it answers, and its kind.
const (
	messageHeaderLen = 16
	// opMsg is the one kind of message a current mongod speaks.
	opMsg = 2013
	// helloID is the id the hello is sent with, which the reply quotes.
	helloID = 1
	// helloTimeout bounds one attempt, so a port that accepts and then says
	// nothing cannot use up the whole start timeout.
	helloTimeout = 2 * time.Second
)

// helloDocument is {hello: 1, $db: "admin"} in BSON: its length, an int32
// element, a string element, and a closing zero.
const helloDocument = "\x1e\x00\x00\x00" +
	"\x10hello\x00\x01\x00\x00\x00" +
	"\x02$db\x00\x06\x00\x00\x00admin\x00" +
	"\x00"

// errNoReply is why a hello failed, inside the NotReadyError a caller sees.
var errNoReply = errors.New("the port accepts connections but no mongod answered a command there")

// helloMessage returns a complete hello command.
func helloMessage() []byte {
	le := binary.LittleEndian
	const flagsAndKindLen = 5
	msg := le.AppendUint32(nil, uint32(messageHeaderLen+flagsAndKindLen+len(helloDocument)))
	msg = le.AppendUint32(msg, helloID)
	msg = le.AppendUint32(msg, 0) // answers nothing
	msg = le.AppendUint32(msg, opMsg)
	msg = le.AppendUint32(msg, 0) // no flags
	msg = append(msg, 0)          // one document follows
	return append(msg, helloDocument...)
}

// answersHello reports whether a message header is mongod's reply to the
// hello. Any reply counts, even an error: only a serving mongod sends one.
func answersHello(header [messageHeaderLen]byte) bool {
	le := binary.LittleEndian
	return le.Uint32(header[8:12]) == helloID && le.Uint32(header[12:16]) == opMsg
}

// hello sends mongod a command over the published port and waits for the
// reply. A connection alone proves nothing: Docker accepts on a published
// port from the moment the container exists, and hangs up if no mongod is
// listening behind it.
func (p readyProbe) hello(ctx context.Context) error {
	dialer := net.Dialer{Timeout: helloTimeout}
	conn, err := dialer.DialContext(ctx, "tcp", addr(p.host, p.port))
	if err != nil {
		return err
	}
	defer conn.Close()
	// Closing the connection is what makes a read give up when ctx ends.
	stop := context.AfterFunc(ctx, func() { _ = conn.Close() })
	defer stop()
	_ = conn.SetDeadline(time.Now().Add(helloTimeout))

	if _, err := conn.Write(helloMessage()); err != nil {
		return fmt.Errorf("%w: %w", errNoReply, err)
	}
	var header [messageHeaderLen]byte
	if _, err := io.ReadFull(conn, header[:]); err != nil {
		return fmt.Errorf("%w: %w", errNoReply, err)
	}
	if !answersHello(header) {
		return errNoReply
	}
	return nil
}
