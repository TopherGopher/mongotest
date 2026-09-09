package dockerapi

import (
	"net/http"
	"strings"
)

// Runtime names the container engine serving the Docker Engine API.
//
// Podman implements the same API, so the endpoints in this package work
// against either. Knowing which one answered is still worth reporting,
// because their version windows differ: Podman capped the compatible API at
// 1.41 from 4.x through 5.7 and raised it to 1.44 in 5.8, where a current
// Docker Engine goes well beyond both. A version error that names the wrong
// product sends the reader looking for a docker daemon they are not running.
type Runtime string

const (
	// RuntimeUnknown means the daemon has not been asked yet, or it
	// identified itself as neither.
	RuntimeUnknown Runtime = ""
	// RuntimeDocker is a Docker Engine.
	RuntimeDocker Runtime = "docker"
	// RuntimePodman is a Podman service on its Docker-compatible endpoint.
	RuntimePodman Runtime = "podman"
)

// Component names reported by GET /version. Docker calls its engine
// component "Engine"; Podman calls its own "Podman Engine". Platform.Name
// cannot be used to tell them apart: Podman puts goos/goarch/distribution
// there, and a moby build from source leaves it empty.
const (
	podmanComponent = "podman"
	dockerComponent = "engine"
	// podmanProductName is what an identified Podman is called when the
	// body did not name it.
	podmanProductName = "Podman Engine"
)

// libpodVersionHeader is set by Podman on every versioned API response and
// has no Docker equivalent. It is the strongest signal available: it does
// not depend on which components the daemon managed to report, and it has
// been stable across Podman 4, 5 and 6.
//
// Spelled in canonical MIME form ("Api", not "API"). http.Header.Get
// canonicalises whatever it is given, and a key that is not already
// canonical costs an allocation on every lookup. This is read once per
// response, so that allocation showed up on every call in the benchmarks.
const libpodVersionHeader = "Libpod-Api-Version"

// LibpodVersionHeader is the response header Podman sets on every API
// response and Docker does not set at all. Exported so a caller building
// their own double can answer the way Podman does.
const LibpodVersionHeader = libpodVersionHeader

// Runtime reports which container engine the daemon identified itself as.
// It is RuntimeUnknown until the daemon has answered something, which any
// public method causes on its first call. Pinning the API version skips
// negotiation, so identification then comes from the Libpod-API-Version
// header that Podman sets on every response rather than from GET /version;
// a Docker daemon sets no such header and is only identified by negotiating.
func (c *Client) Runtime() Runtime {
	c.runtimeMu.Lock()
	defer c.runtimeMu.Unlock()
	return c.runtime
}

// ServerProduct reports the daemon's own description of itself, for example
// "Docker Engine - Community" or "Podman Engine 5.7.1". It is empty until
// negotiation has run, or when the daemon reported nothing usable. Version
// errors name it so the reader knows which daemon reported the window.
func (c *Client) ServerProduct() string {
	c.runtimeMu.Lock()
	defer c.runtimeMu.Unlock()
	return c.product
}

// detectRuntime identifies the daemon from its version response.
// libpodVersion is the Libpod-API-Version response header, which only Podman
// sets and which decides on its own. Failing that the component list is
// authoritative; Platform.Name is only a last resort, and only for Docker,
// because Podman puts a platform triple there and a moby build from source
// leaves it empty.
func detectRuntime(v versionResponse, libpodVersion string) (Runtime, string) {
	for _, comp := range v.Components {
		if strings.Contains(strings.ToLower(comp.Name), podmanComponent) {
			return RuntimePodman, strings.TrimSpace(comp.Name + " " + comp.Version)
		}
	}
	if libpodVersion != "" {
		return RuntimePodman, podmanProductName + " " + libpodVersion
	}
	for _, comp := range v.Components {
		if strings.EqualFold(comp.Name, dockerComponent) {
			return RuntimeDocker, productName(v, comp)
		}
	}
	if strings.Contains(strings.ToLower(v.Platform.Name), "docker") {
		return RuntimeDocker, v.Platform.Name
	}
	return RuntimeUnknown, ""
}

// productName prefers the daemon's own platform string, which for Docker is
// a product name like "Docker Engine - Community", and falls back to the
// component when it is empty.
func productName(v versionResponse, comp versionComponent) string {
	if v.Platform.Name != "" {
		return v.Platform.Name
	}
	return strings.TrimSpace(comp.Name + " " + comp.Version)
}

// runtimeFromHeader identifies Podman from any response, not just the
// version one. Podman sets Libpod-API-Version on everything it serves, which
// is what lets a client that pinned its API version, and therefore never
// calls GET /version, still name the daemon in an error.
func runtimeFromHeader(h http.Header) (Runtime, string) {
	if v := h.Get(libpodVersionHeader); v != "" {
		return RuntimePodman, podmanProductName + " " + v
	}
	return RuntimeUnknown, ""
}
