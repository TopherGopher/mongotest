package dockerapi

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
)

// ContainerConfig is the body of POST /containers/create, reduced to the
// fields mongotest uses. Zero values are omitted from the JSON.
type ContainerConfig struct {
	Image        string              `json:"Image"`
	Cmd          []string            `json:"Cmd,omitempty"`
	Env          []string            `json:"Env,omitempty"`
	Labels       map[string]string   `json:"Labels,omitempty"`
	ExposedPorts map[string]struct{} `json:"ExposedPorts,omitempty"`
	Tty          bool                `json:"Tty,omitempty"`
	OpenStdin    bool                `json:"OpenStdin,omitempty"`
	HostConfig   *HostConfig         `json:"HostConfig,omitempty"`
}

// HostConfig holds the host-side settings of a container.
type HostConfig struct {
	// PortBindings maps a container port such as "27017/tcp" to host
	// addresses it is published on.
	PortBindings map[string][]PortBinding `json:"PortBindings,omitempty"`
	AutoRemove   bool                     `json:"AutoRemove,omitempty"`
}

// PortBinding is one published host address for a container port.
type PortBinding struct {
	HostIP   string `json:"HostIp"`
	HostPort string `json:"HostPort"`
}

// RemoveOptions controls ContainerRemove.
type RemoveOptions struct {
	// Force kills a running container before removing it.
	Force bool
	// RemoveVolumes also removes anonymous volumes attached to the container.
	RemoveVolumes bool
}

// ContainerInspect is the subset of GET /containers/{id}/json we use.
type ContainerInspect struct {
	ID    string `json:"Id"`
	Name  string `json:"Name"`
	State struct {
		Status   string `json:"Status"`
		Running  bool   `json:"Running"`
		ExitCode int    `json:"ExitCode"`
	} `json:"State"`
	Config struct {
		Image  string            `json:"Image"`
		Labels map[string]string `json:"Labels"`
	} `json:"Config"`
	NetworkSettings struct {
		Ports map[string][]PortBinding `json:"Ports"`
	} `json:"NetworkSettings"`
}

// HostPort returns the first host port a container port such as "27017/tcp"
// is published on, or "" when it is not published.
func (ci ContainerInspect) HostPort(containerPort string) string {
	for _, b := range ci.NetworkSettings.Ports[containerPort] {
		if b.HostPort != "" {
			return b.HostPort
		}
	}
	return ""
}

// Top is the process listing of GET /containers/{id}/top.
type Top struct {
	Titles    []string   `json:"Titles"`
	Processes [][]string `json:"Processes"`
}

// ContainerCreate creates a container. name may be empty. A missing image
// yields an error matching ErrNotFound so callers can pull and retry.
func (c *Client) ContainerCreate(ctx context.Context, name string, cfg ContainerConfig) (id string, warnings []string, err error) {
	var q url.Values
	if name != "" {
		q = url.Values{"name": {name}}
	}
	resp, err := c.do(ctx, http.MethodPost, "/containers/create", q, cfg)
	if err != nil {
		return "", nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		return "", nil, newStatusError(resp, http.MethodPost, "/containers/create")
	}
	var out struct {
		ID       string   `json:"Id"`
		Warnings []string `json:"Warnings"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return "", nil, fmt.Errorf("dockerapi: decode container create response: %w", err)
	}
	return out.ID, out.Warnings, nil
}

// ContainerStart starts a created container. An already running container
// (304) is not an error.
func (c *Client) ContainerStart(ctx context.Context, id string) error {
	path := "/containers/" + id + "/start"
	resp, err := c.do(ctx, http.MethodPost, path, nil, nil)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	switch resp.StatusCode {
	case http.StatusNoContent, http.StatusNotModified:
		return nil
	}
	return newStatusError(resp, http.MethodPost, path)
}

// ContainerRemove deletes a container. Use RemoveOptions{Force: true} for a
// running one. A container that no longer exists yields ErrNotFound.
func (c *Client) ContainerRemove(ctx context.Context, id string, opts RemoveOptions) error {
	path := "/containers/" + id
	q := url.Values{}
	if opts.Force {
		q.Set("force", "1")
	}
	if opts.RemoveVolumes {
		q.Set("v", "1")
	}
	resp, err := c.do(ctx, http.MethodDelete, path, q, nil)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		return newStatusError(resp, http.MethodDelete, path)
	}
	return nil
}

// ContainerInspect returns the state and published ports of a container.
func (c *Client) ContainerInspect(ctx context.Context, id string) (ContainerInspect, error) {
	path := "/containers/" + id + "/json"
	var out ContainerInspect
	err := c.getJSON(ctx, path, &out)
	return out, err
}

// ContainerTop lists the processes running inside a container.
func (c *Client) ContainerTop(ctx context.Context, id string) (Top, error) {
	path := "/containers/" + id + "/top"
	var out Top
	err := c.getJSON(ctx, path, &out)
	return out, err
}

// getJSON performs a GET expecting a 200 JSON body decoded into out.
func (c *Client) getJSON(ctx context.Context, path string, out any) error {
	resp, err := c.do(ctx, http.MethodGet, path, nil, nil)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return newStatusError(resp, http.MethodGet, path)
	}
	if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
		return fmt.Errorf("dockerapi: decode GET %s: %w", path, err)
	}
	return nil
}
