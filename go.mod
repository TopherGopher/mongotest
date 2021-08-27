module github.com/tophergopher/mongotest

go 1.15

require (
	docker.io/go-docker v1.0.0
	github.com/docker/go-connections v0.4.0
	github.com/golang/snappy v0.0.4 // indirect
	github.com/jessevdk/go-flags v1.5.0 // indirect
	github.com/klauspost/compress v1.13.3 // indirect
	github.com/mongodb/mongo-tools v0.0.0-20210806132641-f684129d7865
	github.com/sirupsen/logrus v1.8.1
	github.com/stretchr/testify v1.7.0
	github.com/tophergopher/easymongo v0.0.25
	github.com/youmark/pkcs8 v0.0.0-20201027041543-1326539a0a0a // indirect
	go.mongodb.org/mongo-driver v1.7.1
	golang.org/x/crypto v0.0.0-20210711020723-a769d52b0f97 // indirect
	golang.org/x/net v0.0.0-20210805182204-aaa1db679c0d // indirect
	golang.org/x/sync v0.0.0-20210220032951-036812b2e83c // indirect
	golang.org/x/sys v0.0.0-20210806184541-e5e7981a1069 // indirect
	golang.org/x/term v0.0.0-20210615171337-6886f2dfbf5b // indirect
)

replace github.com/docker/docker => github.com/docker/engine v17.12.0-ce-rc1.0.20190717161051-705d9623b7c1+incompatible

// replace github.com/tophergopher/easymongo v0.0.25
