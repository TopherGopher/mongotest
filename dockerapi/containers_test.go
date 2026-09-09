package dockerapi

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"reflect"
	"testing"

	"github.com/tophergopher/mongotest/internal/fakedaemon"
)

func TestContainerCreateBody(t *testing.T) {
	fd, c := newImageClient(t)
	fd.Handle("POST", "/containers/create", func(w http.ResponseWriter, r *http.Request) {
		fakedaemon.JSON(w, 201, map[string]any{"Id": "de686f1e8de9", "Warnings": []string{"w1"}})
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
	if err != nil {
		t.Fatal(err)
	}
	if id != "de686f1e8de9" || !reflect.DeepEqual(warnings, []string{"w1"}) {
		t.Fatalf("id=%q warnings=%v", id, warnings)
	}
	r := fd.Requests()[1]
	if r.Query.Get("name") != "mongotest-34819" {
		t.Fatalf("name query %v", r.Query)
	}
	var got map[string]any
	if err := json.Unmarshal(r.Body, &got); err != nil {
		t.Fatal(err)
	}
	want := map[string]any{
		"Image":        "mongo:8",
		"Cmd":          []any{"--replSet", "rs0"},
		"Labels":       map[string]any{"mongotest": "regression"},
		"ExposedPorts": map[string]any{"27017/tcp": map[string]any{}},
		"HostConfig": map[string]any{
			"PortBindings": map[string]any{
				"27017/tcp": []any{map[string]any{"HostIp": "127.0.0.1", "HostPort": "34819"}},
			},
		},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("create body mismatch\n got: %s\nwant: %v", r.Body, want)
	}
}

func TestContainerCreateOmitsEmptyFieldsAndNoName(t *testing.T) {
	fd, c := newImageClient(t)
	fd.Handle("POST", "/containers/create", func(w http.ResponseWriter, r *http.Request) {
		fakedaemon.JSON(w, 201, map[string]any{"Id": "x"})
	})
	if _, _, err := c.ContainerCreate(context.Background(), "", ContainerConfig{Image: "mongo:8"}); err != nil {
		t.Fatal(err)
	}
	r := fd.Requests()[1]
	if _, has := r.Query["name"]; has {
		t.Fatalf("empty name must not be sent: %v", r.Query)
	}
	if string(r.Body) != `{"Image":"mongo:8"}` {
		t.Fatalf("body %s", r.Body)
	}
}

func TestContainerCreateImageMissingIsNotFound(t *testing.T) {
	fd, c := newImageClient(t)
	fd.Handle("POST", "/containers/create", func(w http.ResponseWriter, r *http.Request) {
		fakedaemon.Error(w, 404, "No such image: mongo:8")
	})
	_, _, err := c.ContainerCreate(context.Background(), "", ContainerConfig{Image: "mongo:8"})
	if !IsNotFound(err) {
		t.Fatalf("want ErrNotFound, got %v", err)
	}
}

func TestContainerStart(t *testing.T) {
	fd, c := newImageClient(t)
	status := 204
	fd.Handle("POST", "/containers/{id}/start", func(w http.ResponseWriter, r *http.Request) {
		if fakedaemon.PathParam(r, "id") == "missing" {
			fakedaemon.Error(w, 404, "No such container: missing")
			return
		}
		w.WriteHeader(status)
	})
	if err := c.ContainerStart(context.Background(), "abc"); err != nil {
		t.Fatalf("204: %v", err)
	}
	status = 304
	if err := c.ContainerStart(context.Background(), "abc"); err != nil {
		t.Fatalf("304 already started must be success: %v", err)
	}
	if err := c.ContainerStart(context.Background(), "missing"); !IsNotFound(err) {
		t.Fatalf("want ErrNotFound, got %v", err)
	}
	if fd.Requests()[1].RawPath != "/v1.44/containers/abc/start" {
		t.Fatalf("path %s", fd.Requests()[1].RawPath)
	}
}

func TestContainerRemoveQuery(t *testing.T) {
	fd, c := newImageClient(t)
	fd.Handle("DELETE", "/containers/{id}", func(w http.ResponseWriter, r *http.Request) {
		if fakedaemon.PathParam(r, "id") == "gone" {
			fakedaemon.Error(w, 404, "No such container: gone")
			return
		}
		w.WriteHeader(204)
	})
	if err := c.ContainerRemove(context.Background(), "abc", RemoveOptions{Force: true, RemoveVolumes: true}); err != nil {
		t.Fatal(err)
	}
	q := fd.Requests()[1].Query
	if q.Get("force") != "1" || q.Get("v") != "1" {
		t.Fatalf("query %v", q)
	}
	if err := c.ContainerRemove(context.Background(), "abc", RemoveOptions{}); err != nil {
		t.Fatal(err)
	}
	if q := fd.Requests()[2].Query; len(q) != 0 {
		t.Fatalf("no flags requested but query is %v", q)
	}
	if err := c.ContainerRemove(context.Background(), "gone", RemoveOptions{Force: true}); !IsNotFound(err) {
		t.Fatalf("want ErrNotFound, got %v", err)
	}
}

func TestContainerInspect(t *testing.T) {
	fd, c := newImageClient(t)
	fd.Handle("GET", "/containers/{id}/json", func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, `{"Id":"abc","Name":"/mongotest-34819","State":{"Status":"running","Running":true,"ExitCode":0},
		  "Config":{"Image":"mongo:8","Labels":{"mongotest":"regression"}},
		  "NetworkSettings":{"Ports":{"27017/tcp":[{"HostIp":"127.0.0.1","HostPort":"34819"}]}}}`)
	})
	info, err := c.ContainerInspect(context.Background(), "abc")
	if err != nil {
		t.Fatal(err)
	}
	if info.ID != "abc" || info.Name != "/mongotest-34819" || !info.State.Running || info.State.Status != "running" {
		t.Fatalf("decoded %+v", info)
	}
	if info.Config.Labels["mongotest"] != "regression" || info.Config.Image != "mongo:8" {
		t.Fatalf("config %+v", info.Config)
	}
	if got := info.HostPort("27017/tcp"); got != "34819" {
		t.Fatalf("HostPort = %q", got)
	}
	if got := info.HostPort("80/tcp"); got != "" {
		t.Fatalf("HostPort for unpublished port = %q", got)
	}
}

func TestContainerTop(t *testing.T) {
	fd, c := newImageClient(t)
	fd.Handle("GET", "/containers/{id}/top", func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, `{"Titles":["UID","PID","CMD"],"Processes":[["999","1234","mongod --bind_ip_all"]]}`)
	})
	top, err := c.ContainerTop(context.Background(), "abc")
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(top.Titles, []string{"UID", "PID", "CMD"}) || len(top.Processes) != 1 || top.Processes[0][2] != "mongod --bind_ip_all" {
		t.Fatalf("decoded %+v", top)
	}
}

func TestContainerServerErrorSurfacesMessage(t *testing.T) {
	fd, c := newImageClient(t)
	fd.Handle("GET", "/containers/{id}/json", func(w http.ResponseWriter, r *http.Request) {
		fakedaemon.Error(w, 500, "driver failed programming external connectivity")
	})
	_, err := c.ContainerInspect(context.Background(), "abc")
	var se *StatusError
	if !errors.As(err, &se) || se.StatusCode != 500 || se.Message != "driver failed programming external connectivity" {
		t.Fatalf("err = %v", err)
	}
	if IsNotFound(err) {
		t.Fatal("500 must not look like not found")
	}
}
