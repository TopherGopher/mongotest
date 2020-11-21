module github.com/tophergopher/mongotest

go 1.15

replace github.com/tophergopher/easymongo => ../easymongo

require (
	docker.io/go-docker v1.0.0
	github.com/docker/distribution v2.7.1+incompatible // indirect
	github.com/docker/go-connections v0.4.0
	github.com/docker/go-units v0.4.0 // indirect
	github.com/gogo/protobuf v1.3.1 // indirect
	github.com/opencontainers/go-digest v1.0.0 // indirect
	github.com/opencontainers/image-spec v1.0.1 // indirect
	github.com/pkg/errors v0.9.1 // indirect
	github.com/sirupsen/logrus v1.7.0
	golang.org/x/net v0.0.0-20201110031124-69a78807bb2b // indirect
)