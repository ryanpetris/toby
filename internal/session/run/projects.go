package run

// Configured-project resolution: validates the launch config's project list,
// resolving each source path and dropping missing or duplicate entries.

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"petris.dev/toby/internal/config"
	"petris.dev/toby/internal/diagnostic/exitcode"
	"petris.dev/toby/internal/diagnostic/warning"
	"petris.dev/toby/internal/tools"
)

func prepareConfiguredProjects(
	warnings *warning.Service,
	home string,
	opts *tools.Options,
) error {
	if opts == nil || len(opts.Projects) == 0 {
		return nil
	}
	projects := make([]tools.ProjectMount, 0, len(opts.Projects))
	seen := map[string]tools.ProjectMount{}
	for _, project := range opts.Projects {
		resolved, exists, err := resolveConfiguredProjectSource(project, home)
		if err != nil {
			return err
		}
		if !exists {
			warnings.Warn(
				warning.ProjectMissing,
				fmt.Sprintf(
					"configured project %q does not exist: %s; skipping it.",
					resolved.Name,
					resolved.Source,
				),
				"project_name", resolved.Name,
				"project_source", resolved.Source,
			)
			continue
		}
		if previous, ok := seen[resolved.Name]; ok {
			warnings.Warn(
				warning.ProjectDuplicate,
				fmt.Sprintf(
					"configured project %q duplicates an earlier project name; using %s and skipping %s.",
					resolved.Name,
					previous.Source,
					resolved.Source,
				),
				"project_name", resolved.Name,
				"selected_source", previous.Source,
				"skipped_source", resolved.Source,
			)
			continue
		}
		seen[resolved.Name] = resolved
		projects = append(projects, resolved)
	}
	if len(projects) == 0 {
		return exitcode.New(1, "launch config projects must include at least one existing project")
	}
	if opts.Env == "" {
		opts.Env = projects[0].Name
	}
	opts.Projects = projects
	return nil
}

func resolveConfiguredProjectSource(project tools.ProjectMount, home string) (tools.ProjectMount, bool, error) {
	name := strings.TrimSpace(project.Name)
	source := strings.TrimSpace(project.Source)
	if source == "" {
		return tools.ProjectMount{}, false, exitcode.New(2, "configured project %s source is required", name)
	}
	abs, err := filepath.Abs(config.ExpandHome(source, home))
	if err != nil {
		return tools.ProjectMount{}, false, err
	}
	resolved := project
	resolved.Name = name
	resolved.Source = abs

	info, err := os.Stat(abs)
	if err != nil || !info.IsDir() {
		return resolved, false, nil
	}
	return resolved, true, nil
}
