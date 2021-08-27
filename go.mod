module github.com/tophergopher/mongotest

go 1.16

require (
	docker.io/go-docker v1.0.0
	github.com/Microsoft/go-winio v0.5.0 // indirect
	github.com/docker/distribution v2.7.1+incompatible // indirect
	github.com/docker/go-connections v0.4.0
	github.com/docker/go-units v0.4.0 // indirect
	github.com/mongodb/mongo-tools v0.0.0-20210806132641-f684129d7865
	github.com/opencontainers/go-digest v1.0.0 // indirect
	github.com/opencontainers/image-spec v1.0.1 // indirect
	github.com/sirupsen/logrus v1.8.1
	github.com/stretchr/testify v1.7.0
	github.com/tophergopher/easymongo v0.0.29
	go.mongodb.org/mongo-driver v1.7.1
)

replace github.com/docker/docker => github.com/docker/engine v17.12.0-ce-rc1.0.20190717161051-705d9623b7c1+incompatible

// replace github.com/tophergopher/easymongo v0.0.29
