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
	// TobyDir is the Toby runtime directory inside the shared home volume. It holds
	// Toby-owned artifacts (aggregated instructions, install scripts, launch
	// descriptors); because it lives under Home, both the home container (which writes
	// it) and the tool container (which mounts the same home volume) see the same files.
	// Generated tool config is written to each tool's real home path, not here.
	TobyDir = "/toby/home/.toby"
	// Instructions holds the aggregated instruction files (bundled + host-configured)
	// that tools reference by path or inline; Toby-owned, wiped and re-rendered per launch.
	Instructions = "/toby/home/.toby/instructions"
	// Scripts holds Toby's internal install/wrapper scripts; Toby-owned, run in the home
	// container during install.
	Scripts = "/toby/home/.toby/scripts"
	// Run holds per-session launch descriptors under the shared home volume.
	Run = "/toby/home/.toby/run"
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
