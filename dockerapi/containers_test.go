package dockerapi

import (
	"context"
	"encoding/json/v2"
	"errors"
	"io"
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tophergopher/mongotest/dockermock"
)

func TestContainerCreateBody(t *testing.T) {
	fd, c := newImageClient(t)
	fd.Handle("POST", "/containers/create", func(w http.ResponseWriter, r *http.Request) {
		dockermock.JSON(w, 201, createResponse{ID: "de686f1e8de9", Warnings: []string{"w1"}})
	})
	cfg := ContainerConfig{
		Image:        "mongo:8",
		Cmd:          []string{"--replSet", "rs0"},
		Labels:       map[string]string{"mongotest": "regression"},
		ExposedPorts: map[string]struct{}{"27017/tcp": {}},
		HostConfig: &HostConfig{
			PortBindings: map[string][]PortBinding{
				"27017/tcp": {{HostIP: "127.0.0.1", HostPort: "34819"}},
			},
		},
	}
	id, warnings, err := c.ContainerCreate(context.Background(), "mongotest-34819", cfg)
	require.NoError(t, err, "create against the fake daemon")
	assert.Equal(t, "de686f1e8de9", id, "the id from the daemon is returned")
	assert.Equal(t, []string{"w1"}, warnings, "daemon warnings are returned")

	r := fd.Requests()[1]
	assert.Equal(t, "mongotest-34819", r.Query.Get("name"), "the container name travels in the query string")

	// Decode the wire body back into the typed config and check each field
	// on its own, so a failure names the field rather than a whole map.
	var sent ContainerConfig
	require.NoError(t, json.Unmarshal(r.Body, &sent), "the create body must be JSON in the ContainerConfig shape")
	assert.Equal(t, "mongo:8", sent.Image, "Image must be sent")
	assert.Equal(t, []string{"--replSet", "rs0"}, sent.Cmd, "Cmd must be sent in order")
	assert.Equal(t, map[string]string{"mongotest": "regression"}, sent.Labels, "Labels must be sent")
	assert.Equal(t, map[string]struct{}{"27017/tcp": {}}, sent.ExposedPorts, "ExposedPorts must be sent with the port/proto key")
	require.NotNil(t, sent.HostConfig, "HostConfig must be sent when set")
	bindings := sent.HostConfig.PortBindings["27017/tcp"]
	require.Len(t, bindings, 1, "exactly one binding for 27017/tcp")
	assert.Equal(t, "127.0.0.1", bindings[0].HostIP, "HostIp must be the loopback address")
	assert.Equal(t, "34819", bindings[0].HostPort, "HostPort must be the chosen port")

	// Wire-level field names matter to the daemon (Go names would not work).
	var raw map[string]any
	require.NoError(t, json.Unmarshal(r.Body, &raw), "the body decodes as generic JSON")
	for _, key := range []string{"Image", "Cmd", "Labels", "ExposedPorts", "HostConfig"} {
		assert.Contains(t, raw, key, "the daemon expects the field spelled %q", key)
	}
	hc := raw["HostConfig"].(map[string]any)
	pb := hc["PortBindings"].(map[string]any)["27017/tcp"].([]any)[0].(map[string]any)
	assert.Contains(t, pb, "HostIp", "the daemon spells it HostIp, not HostIP")
	assert.Contains(t, pb, "HostPort", "HostPort field name")
}

func TestContainerCreateOmitsEmptyFieldsAndNoName(t *testing.T) {
	fd, c := newImageClient(t)
	fd.Handle("POST", "/containers/create", func(w http.ResponseWriter, r *http.Request) {
		dockermock.JSON(w, 201, createResponse{ID: "x"})
	})
	_, _, err := c.ContainerCreate(context.Background(), "", ContainerConfig{Image: "mongo:8"})
	require.NoError(t, err, "create with only an image")
	r := fd.Requests()[1]
	assert.NotContains(t, r.Query, "name", "an empty name must not be sent, the daemon generates one")
	assert.JSONEq(t, `{"Image":"mongo:8"}`, string(r.Body), "unset fields (Cmd, Labels, Tty, HostConfig...) must be omitted from the body")
}

func TestContainerCreateImageMissingIsNotFound(t *testing.T) {
	fd, c := newImageClient(t)
	fd.Handle("POST", "/containers/create", func(w http.ResponseWriter, r *http.Request) {
		dockermock.Error(w, 404, "No such image: mongo:8")
	})
	_, _, err := c.ContainerCreate(context.Background(), "", ContainerConfig{Image: "mongo:8"})
	assert.True(t, IsNotFound(err), "a missing image must be ErrNotFound so callers can pull and retry, got %v", err)
}

func TestContainerStart(t *testing.T) {
	fd, c := newImageClient(t)
	status := 204
	fd.Handle("POST", "/containers/{id}/start", func(w http.ResponseWriter, r *http.Request) {
		if dockermock.PathParam(r, "id") == "missing" {
			dockermock.Error(w, 404, "No such container: missing")
			return
		}
		w.WriteHeader(status)
	})
	require.NoError(t, c.ContainerStart(context.Background(), "abc"), "204 is a successful start")
	status = 304
	require.NoError(t, c.ContainerStart(context.Background(), "abc"), "304 (already started) must also be success")
	err := c.ContainerStart(context.Background(), "missing")
	assert.True(t, IsNotFound(err), "starting an unknown container must be ErrNotFound, got %v", err)
	assert.Equal(t, "/v1.44/containers/abc/start", fd.Requests()[1].RawPath, "start hits /containers/{id}/start under the negotiated version")
}

func TestContainerRemoveQuery(t *testing.T) {
	fd, c := newImageClient(t)
	fd.Handle("DELETE", "/containers/{id}", func(w http.ResponseWriter, r *http.Request) {
		if dockermock.PathParam(r, "id") == "gone" {
			dockermock.Error(w, 404, "No such container: gone")
			return
		}
		w.WriteHeader(204)
	})
	require.NoError(t, c.ContainerRemove(context.Background(), "abc", RemoveOptions{Force: true, RemoveVolumes: true}), "forced remove")
	q := fd.Requests()[1].Query
	assert.Equal(t, "1", q.Get("force"), "Force must be sent as force=1")
	assert.Equal(t, "1", q.Get("v"), "RemoveVolumes must be sent as v=1")
	require.NoError(t, c.ContainerRemove(context.Background(), "abc", RemoveOptions{}), "plain remove")
	assert.Empty(t, fd.Requests()[2].Query, "no flags requested means no query parameters")
	err := c.ContainerRemove(context.Background(), "gone", RemoveOptions{Force: true})
	assert.True(t, IsNotFound(err), "removing an unknown container must be ErrNotFound, got %v", err)
}

func TestContainerInspect(t *testing.T) {
	fd, c := newImageClient(t)
	fd.Handle("GET", "/containers/{id}/json", func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, `{"Id":"abc","Name":"/mongotest-34819","State":{"Status":"running","Running":true,"ExitCode":0},
		  "Config":{"Image":"mongo:8","Labels":{"mongotest":"regression"}},
		  "NetworkSettings":{"Ports":{"27017/tcp":[{"HostIp":"127.0.0.1","HostPort":"34819"}]}}}`)
	})
	info, err := c.ContainerInspect(context.Background(), "abc")
	require.NoError(t, err, "inspect against the fake daemon")
	assert.Equal(t, "abc", info.ID, "Id decodes into ID")
	assert.Equal(t, "/mongotest-34819", info.Name, "the daemon reports names with a leading slash")
	assert.Equal(t, ContainerState{Status: "running", Running: true, ExitCode: 0}, info.State, "State decodes into ContainerState")
	assert.Equal(t, InspectedConfig{Image: "mongo:8", Labels: map[string]string{"mongotest": "regression"}}, info.Config, "Config decodes into InspectedConfig")
	assert.Equal(t, "34819", info.HostPort("27017/tcp"), "HostPort returns the published host port")
	assert.Empty(t, info.HostPort("80/tcp"), "HostPort is empty for a port that is not published")
}

func TestContainerTop(t *testing.T) {
	fd, c := newImageClient(t)
	fd.Handle("GET", "/containers/{id}/top", func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, `{"Titles":["UID","PID","CMD"],"Processes":[["999","1234","mongod --bind_ip_all"]]}`)
	})
	top, err := c.ContainerTop(context.Background(), "abc")
	require.NoError(t, err, "top against the fake daemon")
	assert.Equal(t, []string{"UID", "PID", "CMD"}, top.Titles, "Titles decode")
	require.Len(t, top.Processes, 1, "one process row")
	assert.Equal(t, "mongod --bind_ip_all", top.Processes[0][2], "the CMD column carries the mongod command line")
}

func TestContainerServerErrorSurfacesMessage(t *testing.T) {
	fd, c := newImageClient(t)
	fd.Handle("GET", "/containers/{id}/json", func(w http.ResponseWriter, r *http.Request) {
		dockermock.Error(w, 500, "driver failed programming external connectivity")
	})
	_, err := c.ContainerInspect(context.Background(), "abc")
	var se *StatusError
	require.True(t, errors.As(err, &se), "a 500 must be a StatusError")
	assert.Equal(t, 500, se.StatusCode, "the status code is carried")
	assert.Equal(t, "driver failed programming external connectivity", se.Message, "the daemon's message is carried verbatim")
	assert.False(t, IsNotFound(err), "a 500 must never look like not found")
}
