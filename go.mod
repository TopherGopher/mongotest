module github.com/tophergopher/mongotest

go 1.15

require (
	docker.io/go-docker v1.0.0
	github.com/Microsoft/go-winio v0.4.15 // indirect
	github.com/docker/distribution v2.7.1+incompatible // indirect
	github.com/docker/docker v17.12.0-ce-rc1.0.20200531234253-77e06fda0c94+incompatible // indirect
	github.com/docker/go-connections v0.4.0
	github.com/docker/go-units v0.4.0 // indirect
	github.com/gogo/protobuf v1.3.1 // indirect
	github.com/opencontainers/go-digest v1.0.0 // indirect
	github.com/opencontainers/image-spec v1.0.1 // indirect
	github.com/sirupsen/logrus v1.7.0
	github.com/stretchr/testify v1.6.1
	github.com/tophergopher/easymongo v0.0.6
	gotest.tools v2.2.0+incompatible // indirect
)

replace github.com/docker/docker => github.com/docker/engine v17.12.0-ce-rc1.0.20190717161051-705d9623b7c1+incompatible

// replace github.com/docker/docker/internal/testutil => gotest.tools/v3 v3.0.0

// replace github.com/tophergopher/easymongo => ../easymongo

// replace github.com/docker/go-connectons v0.4.0 => github.com/docker/go-connections v0.4.0
