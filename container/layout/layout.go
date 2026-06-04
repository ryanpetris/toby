// Package layout defines the fixed in-container filesystem layout every Toby
// sandbox uses. The base paths are constants so they can be changed in one
// place rather than threaded through a per-instance path struct.
//
// Callers address container-interior paths either as absolute paths under these
// constants (e.g. Bin, Workspace) or with a leading "~"/"~/" that Expand resolves
// to Home. Host paths are never expanded here — callers supply absolute host paths.
package layout

import (
	pathpkg "path"
	"strings"
)

const (
	// Root is the sandbox root inside the container.
	Root = "/toby"
	// Home is the container $HOME.
	Home = "/toby/home"
	// Workspace is where project mounts live.
	Workspace = "/toby/workspace"
	// Bin is where the Toby helper binary lives.
	Bin = "/toby/bin"
	// Context is where generated configuration and instructions live.
	Context = "/toby/context"
	// DockerSocket is the Docker daemon socket path (host and container).
	DockerSocket = "/var/run/docker.sock"
)

// Expand resolves a container-interior path. A leading "~" or "~/" expands to
// Home; any other value (already absolute) is returned unchanged.
func Expand(path string) string {
	if path == "~" {
		return Home
	}
	if strings.HasPrefix(path, "~/") {
		return pathpkg.Join(Home, path[2:])
	}

	return path
}
