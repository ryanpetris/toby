package lifecycle

// Collects native generated-file contributions from the active concrete tools
// before the Bubblewrap run plan is built.

import (
	"fmt"

	"petris.dev/toby/internal/toolfiles"
	"petris.dev/toby/internal/tools"
)

// ToolFiles returns one validated deterministic file set containing every
// contribution in tool dependency order. Each contribution must retain the
// selected tool's identity and the launch's requested ownership.
func ToolFiles(
	set *tools.Toolset,
	ownership toolfiles.Ownership,
) ([]toolfiles.File, error) {
	registry := toolfiles.NewRegistry()
	if set == nil {
		return registry.Files(), nil
	}

	for _, tool := range set.OrderedTools() {
		contributor, ok := tool.(toolfiles.Contributor)
		if !ok {
			continue
		}

		files, err := contributor.ToolFiles(ownership)
		if err != nil {
			return nil, fmt.Errorf(
				"collect native files from %s: %w",
				tool.Name(),
				err,
			)
		}
		for _, file := range files {
			if file.Owner != tool.Name() {
				return nil, fmt.Errorf(
					"collect native files from %s: file %q declares owner %q",
					tool.Name(),
					file.Target,
					file.Owner,
				)
			}
			if file.UID != ownership.UID || file.GID != ownership.GID {
				return nil, fmt.Errorf(
					"collect native files from %s: file %q declares ownership %d:%d, want %d:%d",
					tool.Name(),
					file.Target,
					file.UID,
					file.GID,
					ownership.UID,
					ownership.GID,
				)
			}
			if err := registry.Register(file); err != nil {
				return nil, fmt.Errorf(
					"collect native files from %s: %w",
					tool.Name(),
					err,
				)
			}
		}
	}

	return registry.Files(), nil
}
