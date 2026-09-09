package mobyclient

import (
	"context"
	"net/http"
	"net/netip"

	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/api/types/network"
	"github.com/moby/moby/client"

	"github.com/tophergopher/mongotest/dockerclient"
)

// ContainerCreate creates a container and returns its id along with any
// warnings the daemon reported. name may be empty, in which case the daemon
// generates one. When the image is not present locally the error is
// ErrNotFound, so callers can pull and retry.
func (c *Client) ContainerCreate(ctx context.Context, name string, cfg dockerclient.ContainerConfig) (string, []string, error) {
	if err := cfg.Validate(); err != nil {
		return "", nil, err
	}
	name, err := dockerclient.CheckContainerName(name)
	if err != nil {
		return "", nil, err
	}
	mcfg, hostCfg, err := toMobyConfig(cfg)
	if err != nil {
		return "", nil, err
	}
	res, err := c.cli.ContainerCreate(ctx, client.ContainerCreateOptions{Config: mcfg, HostConfig: hostCfg, Name: name})
	if err != nil {
		return "", nil, c.mapErr(http.MethodPost, "/containers/create", err)
	}
	return res.ID, res.Warnings, nil
}

// ContainerStart starts a created container. Starting one that is already
// running is not an error.
func (c *Client) ContainerStart(ctx context.Context, id string) error {
	id, err := dockerclient.CheckID("container", id)
	if err != nil {
		return err
	}
	if _, err := c.cli.ContainerStart(ctx, id, client.ContainerStartOptions{}); err != nil {
		return c.mapErr(http.MethodPost, "/containers/"+id+"/start", err)
	}
	return nil
}

// ContainerRemove deletes a container. Use RemoveOptions{Force: true} for
// one that is still running. A container that is already gone is
// ErrNotFound.
func (c *Client) ContainerRemove(ctx context.Context, id string, opts dockerclient.RemoveOptions) error {
	id, err := dockerclient.CheckID("container", id)
	if err != nil {
		return err
	}
	mopts := client.ContainerRemoveOptions{Force: opts.Force, RemoveVolumes: opts.RemoveVolumes}
	if _, err := c.cli.ContainerRemove(ctx, id, mopts); err != nil {
		return c.mapErr(http.MethodDelete, "/containers/"+id, err)
	}
	return nil
}

// ContainerInspect returns the state of a container and the host addresses
// its ports are published on.
func (c *Client) ContainerInspect(ctx context.Context, id string) (dockerclient.ContainerInspect, error) {
	id, err := dockerclient.CheckID("container", id)
	if err != nil {
		return dockerclient.ContainerInspect{}, err
	}
	res, err := c.cli.ContainerInspect(ctx, id, client.ContainerInspectOptions{})
	if err != nil {
		return dockerclient.ContainerInspect{}, c.mapErr(http.MethodGet, "/containers/"+id+"/json", err)
	}
	return toDockerclientInspect(res.Container), nil
}

// ContainerTop lists the processes running inside a container.
func (c *Client) ContainerTop(ctx context.Context, id string) (dockerclient.Top, error) {
	id, err := dockerclient.CheckID("container", id)
	if err != nil {
		return dockerclient.Top{}, err
	}
	res, err := c.cli.ContainerTop(ctx, id, client.ContainerTopOptions{})
	if err != nil {
		return dockerclient.Top{}, c.mapErr(http.MethodGet, "/containers/"+id+"/top", err)
	}
	return dockerclient.Top{Titles: res.Titles, Processes: res.Processes}, nil
}

// toMobyConfig translates a dockerclient.ContainerConfig, already validated
// by ContainerConfig.Validate, into the container.Config and
// container.HostConfig the moby client expects. Translation failures here
// would mean the two validation grammars disagree; they are still reported
// as InvalidArgumentError rather than left to panic.
func toMobyConfig(cfg dockerclient.ContainerConfig) (*container.Config, *container.HostConfig, error) {
	mcfg := &container.Config{
		Image:     cfg.Image,
		Cmd:       cfg.Cmd,
		Env:       cfg.Env,
		Labels:    cfg.Labels,
		Tty:       cfg.Tty,
		OpenStdin: cfg.OpenStdin,
	}
	if len(cfg.ExposedPorts) > 0 {
		ports := make(network.PortSet, len(cfg.ExposedPorts))
		for k := range cfg.ExposedPorts {
			p, err := network.ParsePort(k)
			if err != nil {
				return nil, nil, dockerclient.InvalidArgument("ContainerConfig.ExposedPorts key", k, err.Error(), `use keys like "27017/tcp"`)
			}
			ports[p] = struct{}{}
		}
		mcfg.ExposedPorts = ports
	}
	if cfg.HostConfig == nil {
		return mcfg, nil, nil
	}
	hostCfg := &container.HostConfig{AutoRemove: cfg.HostConfig.AutoRemove}
	if len(cfg.HostConfig.PortBindings) > 0 {
		pm := make(network.PortMap, len(cfg.HostConfig.PortBindings))
		for k, bindings := range cfg.HostConfig.PortBindings {
			p, err := network.ParsePort(k)
			if err != nil {
				return nil, nil, dockerclient.InvalidArgument("HostConfig.PortBindings key", k, err.Error(), `use keys like "27017/tcp"`)
			}
			converted := make([]network.PortBinding, len(bindings))
			for i, b := range bindings {
				var addr netip.Addr
				if b.HostIP != "" {
					a, err := netip.ParseAddr(b.HostIP)
					if err != nil {
						return nil, nil, dockerclient.InvalidArgument("HostConfig.PortBindings["+k+"].HostIP", b.HostIP,
							"it is not an IP address", `use "127.0.0.1" to publish on loopback only, or "" for all interfaces`)
					}
					addr = a
				}
				converted[i] = network.PortBinding{HostIP: addr, HostPort: b.HostPort}
			}
			pm[p] = converted
		}
		hostCfg.PortBindings = pm
	}
	return mcfg, hostCfg, nil
}

// toDockerclientInspect translates the moby client's inspect response into
// the shared dockerclient.ContainerInspect shape.
func toDockerclientInspect(ci container.InspectResponse) dockerclient.ContainerInspect {
	out := dockerclient.ContainerInspect{ID: ci.ID, Name: ci.Name}
	if ci.State != nil {
		out.State = dockerclient.ContainerState{
			Status:   string(ci.State.Status),
			Running:  ci.State.Running,
			ExitCode: ci.State.ExitCode,
		}
	}
	if ci.Config != nil {
		out.Config = dockerclient.InspectedConfig{Image: ci.Config.Image, Labels: ci.Config.Labels}
	}
	if ci.NetworkSettings != nil {
		out.NetworkSettings = dockerclient.NetworkSettings{Ports: toDockerclientPorts(ci.NetworkSettings.Ports)}
	}
	return out
}

// toDockerclientPorts translates a network.PortMap into the string-keyed
// shape dockerclient.NetworkSettings uses.
func toDockerclientPorts(pm network.PortMap) map[string][]dockerclient.PortBinding {
	if len(pm) == 0 {
		return nil
	}
	out := make(map[string][]dockerclient.PortBinding, len(pm))
	for port, bindings := range pm {
		converted := make([]dockerclient.PortBinding, len(bindings))
		for i, b := range bindings {
			var ip string
			if b.HostIP.IsValid() {
				ip = b.HostIP.String()
			}
			converted[i] = dockerclient.PortBinding{HostIP: ip, HostPort: b.HostPort}
		}
		out[port.String()] = converted
	}
	return out
}
