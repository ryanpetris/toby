package runtime

// Tool-container bind construction: translates the session's project (workspace)
// mounts and the tools' declared host binds into the Docker bind list the tool
// container receives (layered under its home + bin volume mounts by CreateTool).

import (
	"errors"
	"os"

	dmount "github.com/moby/moby/api/types/mount"

	"petris.dev/toby/container/mount"
)

// ToolBinds builds the tool container's host binds: the workspace project mounts
// followed by the tools' declared binds (skipping missing optional ones).
func ToolBinds(binds []mount.Bind, projects []ProjectMount) []dmount.Mount {
	out := make([]dmount.Mount, 0, len(binds)+len(projects))
	for _, project := range projects {
		out = append(out, dmount.Mount{Type: dmount.TypeBind, Source: project.HostPath, Target: project.SandboxPath})
	}
	for _, bind := range binds {
		if bind.Optional {
			if _, err := os.Stat(bind.HostPath); err != nil && errors.Is(err, os.ErrNotExist) {
				continue
			}
		}
		out = append(out, dmount.Mount{Type: dmount.TypeBind, Source: bind.HostPath, Target: bind.Target, ReadOnly: bind.Access == mount.AccessReadOnly})
	}
	return out
}
