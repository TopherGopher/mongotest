package dockerclienttest

import (
	"archive/tar"
	"io"
	"time"
)

// tarWriter is a very small archive/tar wrapper, so the benchmark suite can
// build a fixture archive without depending on a client's internals.
type tarWriter struct{ tw *tar.Writer }

func newTarWriter(w io.Writer) *tarWriter { return &tarWriter{tw: tar.NewWriter(w)} }

func (t *tarWriter) write(name string, body []byte) error {
	hdr := &tar.Header{
		Name: name, Mode: 0o644, Size: int64(len(body)),
		Typeflag: tar.TypeReg, ModTime: time.Now(),
	}
	if err := t.tw.WriteHeader(hdr); err != nil {
		return err
	}
	_, err := t.tw.Write(body)
	return err
}

func (t *tarWriter) close() error { return t.tw.Close() }
