package dockerapi

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
)

// Stream types used in the daemon's multiplexed attach format.
const (
	streamStdin  byte = 0
	streamStdout byte = 1
	streamStderr byte = 2
)

// maxFrame bounds a single frame to guard against a corrupt header.
const maxFrame = 64 << 20

// demux reads the daemon's multiplexed stream (8-byte header: stream type,
// three zero bytes, big-endian uint32 payload length; then the payload)
// until EOF and returns the stdout and stderr bytes. A stream that ends in
// the middle of a frame yields io.ErrUnexpectedEOF.
func demux(r io.Reader) (stdout, stderr []byte, err error) {
	var out, errOut bytes.Buffer
	hdr := make([]byte, 8)
	for {
		if _, err := io.ReadFull(r, hdr); err != nil {
			if errors.Is(err, io.EOF) {
				return out.Bytes(), errOut.Bytes(), nil
			}
			return out.Bytes(), errOut.Bytes(), fmt.Errorf("dockerapi: reading stream frame header: %w", err)
		}
		size := binary.BigEndian.Uint32(hdr[4:8])
		if size > maxFrame {
			return out.Bytes(), errOut.Bytes(), fmt.Errorf("dockerapi: stream frame of %d bytes exceeds limit", size)
		}
		var dst *bytes.Buffer
		switch hdr[0] {
		case streamStdin, streamStdout:
			dst = &out
		case streamStderr:
			dst = &errOut
		default:
			return out.Bytes(), errOut.Bytes(), fmt.Errorf("dockerapi: unknown stream type %d in attach frame", hdr[0])
		}
		if _, err := io.CopyN(dst, r, int64(size)); err != nil {
			if errors.Is(err, io.EOF) {
				err = io.ErrUnexpectedEOF
			}
			return out.Bytes(), errOut.Bytes(), fmt.Errorf("dockerapi: reading stream frame payload: %w", err)
		}
	}
}
