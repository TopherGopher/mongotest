package dockerapi

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"strings"
)

// Stream types used in the daemon's multiplexed attach format.
const (
	streamStdin  byte = 0
	streamStdout byte = 1
	streamStderr byte = 2
	// streamSystemErr carries an error message from the daemon itself, for
	// example when the container stops mid-exec.
	streamSystemErr byte = 3
)

// maxFrame bounds a single frame to guard against a corrupt header.
const maxFrame = 64 << 20

// demux reads the daemon's multiplexed stream (8-byte header: stream type,
// three zero bytes, big-endian uint32 payload length; then the payload)
// until EOF, copying stdout frames to out and stderr frames to errOut as they
// arrive. A stream that ends in the middle of a frame yields a StreamError
// wrapping io.ErrUnexpectedEOF; a system error frame yields a StreamError
// carrying the daemon's message.
func demux(r io.Reader, out, errOut io.Writer) error {
	hdr := make([]byte, 8)
	for {
		if _, err := io.ReadFull(r, hdr); err != nil {
			if errors.Is(err, io.EOF) {
				return nil
			}
			return &StreamError{Problem: "reading an exec stream frame header", Err: err}
		}
		size := binary.BigEndian.Uint32(hdr[4:8])
		if size > maxFrame {
			return &StreamError{Problem: fmt.Sprintf("an exec stream frame claims %d bytes, above the %d byte limit", size, maxFrame)}
		}
		var dst io.Writer
		var sysErr bytes.Buffer
		switch hdr[0] {
		case streamStdin, streamStdout:
			dst = out
		case streamStderr:
			dst = errOut
		case streamSystemErr:
			dst = &sysErr
		default:
			return &StreamError{Problem: fmt.Sprintf("unknown stream type %d in an exec stream frame", hdr[0])}
		}
		if _, err := io.CopyN(dst, r, int64(size)); err != nil {
			if errors.Is(err, io.EOF) {
				err = io.ErrUnexpectedEOF
			}
			return &StreamError{Problem: "reading an exec stream frame payload", Err: err}
		}
		if hdr[0] == streamSystemErr {
			return &StreamError{Problem: "the daemon reported an error in the exec stream: " + strings.TrimSpace(sysErr.String())}
		}
	}
}
