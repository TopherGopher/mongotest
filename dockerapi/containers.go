package dockerapi

import (
	"context"
	"encoding/json/v2"
	"net/http"
	"net/url"
)

// ContainerConfig is the body of POST /containers/create, reduced to the
// fields mongotest uses. Unset fields are omitted from the JSON.
type ContainerConfig struct {
	// Image is the reference or id to run; required.
	Image string `json:"Image"`
	// Cmd overrides the image command. For the official mongo image the
	// entrypoint prepends "mongod", so pass flags only.
	Cmd []string `json:"Cmd,omitempty"`
	// Env holds KEY=value entries.
	Env []string `json:"Env,omitempty"`
	// Labels are attached to the container; mongotest always sets
	// mongotest=regression so stray containers can be found.
	Labels map[string]string `json:"Labels,omitempty"`
	// ExposedPorts lists container ports such as "27017/tcp".
	ExposedPorts map[string]struct{} `json:"ExposedPorts,omitempty"`
	Tty          bool                `json:"Tty,omitzero"`
	OpenStdin    bool                `json:"OpenStdin,omitzero"`
	// HostConfig holds host-side settings; nil means daemon defaults.
	HostConfig *HostConfig `json:"HostConfig,omitempty"`
}

// HostConfig holds the host-side settings of a container.
type HostConfig struct {
	// PortBindings maps a container port such as "27017/tcp" to the host
	// addresses it is published on.
	PortBindings map[string][]PortBinding `json:"PortBindings,omitempty"`
	// AutoRemove asks the daemon to delete the container when it exits.
	AutoRemove bool `json:"AutoRemove,omitzero"`
}

// PortBinding is one published host address for a container port.
type PortBinding struct {
	// HostIP is the interface to publish on; "127.0.0.1" keeps it local.
	HostIP string `json:"HostIp"`
	// HostPort is the host port, or "" to let the daemon choose one (read it
	// back with ContainerInspect).
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
	ID              string          `json:"Id"`
	Name            string          `json:"Name"`
	State           ContainerState  `json:"State"`
	Config          InspectedConfig `json:"Config"`
	NetworkSettings NetworkSettings `json:"NetworkSettings"`
}

// ContainerState is the runtime state reported by inspect.
type ContainerState struct {
	// Status is one of created, running, paused, restarting, removing,
	// exited or dead.
	Status   string `json:"Status"`
	Running  bool   `json:"Running"`
	ExitCode int    `json:"ExitCode"`
}

// InspectedConfig is the part of the container's configuration that inspect
// reports and mongotest reads back.
type InspectedConfig struct {
	Image  string            `json:"Image"`
	Labels map[string]string `json:"Labels"`
}

// NetworkSettings holds the published ports reported by inspect.
type NetworkSettings struct {
	// Ports maps a container port such as "27017/tcp" to the host addresses
	// it is published on; nil for unpublished ports.
	Ports map[string][]PortBinding `json:"Ports"`
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

// createResponse is the body of a successful POST /containers/create.
type createResponse struct {
	ID       string   `json:"Id"`
	Warnings []string `json:"Warnings"`
}

// ContainerCreate creates a container. name may be empty. The config is
// validated first (InvalidArgumentError); a missing image yields an error
// matching ErrNotFound so callers can pull and retry.
func (c *Client) ContainerCreate(ctx context.Context, name string, cfg ContainerConfig) (id string, warnings []string, err error) {
	if err := cfg.validate(); err != nil {
		return "", nil, err
	}
	name, err = checkContainerName(name)
	if err != nil {
		return "", nil, err
	}
	var q url.Values
	if name != "" {
		q = url.Values{"name": {name}}
	}
	const path = "/containers/create"
	resp, err := c.do(ctx, http.MethodPost, path, q, cfg)
	if err != nil {
		return "", nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		if resp.StatusCode >= 400 {
			return "", nil, newStatusError(resp, http.MethodPost, path)
		}
		return "", nil, unexpectedStatus(http.MethodPost, path, resp.StatusCode)
	}
	var out createResponse
	if err := json.UnmarshalRead(resp.Body, &out); err != nil {
		return "", nil, decodeError(http.MethodPost, path, err)
	}
	return out.ID, out.Warnings, nil
}

// ContainerStart starts a created container. An already running container
// (304) is not an error.
func (c *Client) ContainerStart(ctx context.Context, id string) error {
	id, err := checkID("container", id)
	if err != nil {
		return err
	}
	path := "/containers/" + id + "/start"
	resp, err := c.do(ctx, http.MethodPost, path, nil, nil)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	switch {
	case resp.StatusCode == http.StatusNoContent, resp.StatusCode == http.StatusNotModified:
		return nil
	case resp.StatusCode >= 400:
		return newStatusError(resp, http.MethodPost, path)
	}
	return unexpectedStatus(http.MethodPost, path, resp.StatusCode)
}

// ContainerRemove deletes a container. Use RemoveOptions{Force: true} for a
// running one. A container that no longer exists yields ErrNotFound.
func (c *Client) ContainerRemove(ctx context.Context, id string, opts RemoveOptions) error {
	id, err := checkID("container", id)
	if err != nil {
		return err
	}
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
	switch {
	case resp.StatusCode == http.StatusNoContent:
		return nil
	case resp.StatusCode >= 400:
		return newStatusError(resp, http.MethodDelete, path)
	}
	return unexpectedStatus(http.MethodDelete, path, resp.StatusCode)
}

// ContainerInspect returns the state and published ports of a container.
func (c *Client) ContainerInspect(ctx context.Context, id string) (ContainerInspect, error) {
	var out ContainerInspect
	id, err := checkID("container", id)
	if err != nil {
		return out, err
	}
	err = c.getJSON(ctx, "/containers/"+id+"/json", &out)
	return out, err
}

// ContainerTop lists the processes running inside a container.
func (c *Client) ContainerTop(ctx context.Context, id string) (Top, error) {
	var out Top
	id, err := checkID("container", id)
	if err != nil {
		return out, err
	}
	err = c.getJSON(ctx, "/containers/"+id+"/top", &out)
	return out, err
}

// getJSON performs a GET expecting a 200 JSON body decoded into out.
func (c *Client) getJSON(ctx context.Context, path string, out any) error {
	resp, err := c.do(ctx, http.MethodGet, path, nil, nil)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	switch {
	case resp.StatusCode == http.StatusOK:
	case resp.StatusCode >= 400:
		return newStatusError(resp, http.MethodGet, path)
	default:
		return unexpectedStatus(http.MethodGet, path, resp.StatusCode)
	}
	if err := json.UnmarshalRead(resp.Body, out); err != nil {
		return decodeError(http.MethodGet, path, err)
	}
	return nil
}
