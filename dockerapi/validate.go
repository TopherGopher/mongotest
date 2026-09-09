package dockerapi

import "github.com/tophergopher/mongotest/dockerclient"

// Validation is shared by every client, so it lives in dockerclient; these
// are the names this package's own code and tests use.
type imageRef = dockerclient.ImageRef

const refNameMaxLength = dockerclient.RefNameMaxLength

var (
	parseImageRef      = dockerclient.ParseImageRef
	checkID            = dockerclient.CheckID
	checkContainerName = dockerclient.CheckContainerName
	checkImageRefOrID  = dockerclient.CheckImageRefOrID
	checkPortKey       = dockerclient.CheckPortKey
	checkHostPort      = dockerclient.CheckHostPort
	validateFiles      = dockerclient.ValidateFiles
)
