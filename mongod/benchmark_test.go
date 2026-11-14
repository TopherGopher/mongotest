package mongod_test

import (
	"context"
	"testing"

	"github.com/tophergopher/mongotest/mongod"
)

// BenchmarkStart measures what Start costs apart from Docker: a suite pays
// it once per container, often in parallel.
func BenchmarkStart(b *testing.B) {
	m, _ := readyMock(b)
	ctx := context.Background()
	opts := []mongod.Option{mongod.WithDocker(m), mongod.WithHostIP("127.0.0.1")}
	b.ReportAllocs()
	for b.Loop() {
		c, err := mongod.Start(ctx, opts...)
		if err != nil {
			b.Fatal(err)
		}
		if err := c.Stop(ctx); err != nil {
			b.Fatal(err)
		}
		m.Reset()
	}
}

func BenchmarkParseMongoImage(b *testing.B) {
	b.ReportAllocs()
	for b.Loop() {
		if _, err := mongod.ParseMongoImage("public.ecr.aws/docker/library/mongo:8.0"); err != nil {
			b.Fatal(err)
		}
	}
}
