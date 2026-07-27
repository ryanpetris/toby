package run

// Builds the bounded, sandbox-safe session snapshot handed to Toby's
// agent-hosted built-in MCP server for one native launch.

import (
	"fmt"
	"sort"

	mcpconfig "petris.dev/toby/internal/config/mcp"
	"petris.dev/toby/internal/config/session"
	"petris.dev/toby/internal/sandbox/bwrap"
	"petris.dev/toby/internal/sandbox/layout"
	"petris.dev/toby/internal/sandbox/mount"
	"petris.dev/toby/internal/tobymcp"
	"petris.dev/toby/internal/tools"
)

type nativeSnapshotInput struct {
	Debug        bool
	Environment  string
	Profile      string
	RootFSDigest string
	Workdir      string
	Registry     *tools.Registry
	Toolset      *tools.Toolset
	Projects     []bwrap.Project
	Mounts       []mount.Entry
	Binds        []mount.Bind
	MCP          mcpconfig.Config
}

func buildNativeSessionSnapshot(
	input nativeSnapshotInput,
) (tobymcp.SessionSnapshot, error) {
	if input.Registry == nil {
		return tobymcp.SessionSnapshot{}, fmt.Errorf(
			"build native session snapshot: tool registry is required",
		)
	}
	if input.Toolset == nil || input.Toolset.Primary() == nil {
		return tobymcp.SessionSnapshot{}, fmt.Errorf(
			"build native session snapshot: primary tool is required",
		)
	}

	snapshot := tobymcp.SessionSnapshot{
		Debug: input.Debug,
		Runtime: tobymcp.SessionRuntime{
			Name:         input.Environment,
			Profile:      input.Profile,
			Runtime:      "bubblewrap",
			RootFSDigest: input.RootFSDigest,
			Network:      "host",
			Home:         layout.Home,
			Workspace:    layout.Workspace,
			Root:         layout.Root,
			Bin:          layout.Bin,
			Workdir:      input.Workdir,
		},
		Tools:    nativeSnapshotTools(input.Registry, input.Toolset),
		Projects: nativeSnapshotProjects(input.Projects),
		Mounts:   nativeSnapshotMounts(input.Mounts),
		Binds:    nativeSnapshotBinds(input.Binds),
		MCPs:     nativeSnapshotMCPs(input.MCP),
	}
	if err := snapshot.Validate(); err != nil {
		return tobymcp.SessionSnapshot{}, fmt.Errorf(
			"build native session snapshot: %w",
			err,
		)
	}

	return snapshot.Clone(), nil
}

func nativeSnapshotTools(
	registry *tools.Registry,
	set *tools.Toolset,
) tobymcp.SessionTools {
	names := registry.ToolNames()
	available := make([]tobymcp.SessionTool, 0, len(names))
	groups := make(map[string]string, len(names))
	for _, name := range names {
		tool, found := registry.Get(name)
		if !found {
			continue
		}

		contextGroups := append([]string(nil), tool.ContextGroups()...)
		sort.Strings(contextGroups)
		available = append(available, tobymcp.SessionTool{
			Name:          name,
			Launchable:    tool.LaunchHelp() != "",
			ContextGroups: contextGroups,
		})
		if group := tool.Group(); group != "" {
			groups[name] = group
		}
	}
	if len(groups) == 0 {
		groups = nil
	}

	primary := ""
	if set != nil && set.Primary() != nil {
		primary = set.Primary().Name()
	}

	return tobymcp.SessionTools{
		Primary:   primary,
		Active:    sortedStrings(set.OrderedToolNames()),
		Available: available,
		Groups:    groups,
	}
}

func nativeSnapshotProjects(
	projects []bwrap.Project,
) []tobymcp.SessionProject {
	result := make([]tobymcp.SessionProject, len(projects))
	for index, project := range projects {
		result[index] = tobymcp.SessionProject{
			Name:        project.Name,
			SandboxPath: project.Target,
		}
	}
	sort.Slice(result, func(i, j int) bool {
		return result[i].Name < result[j].Name
	})

	return result
}

func nativeSnapshotMounts(
	entries []mount.Entry,
) []tobymcp.SessionMount {
	result := make([]tobymcp.SessionMount, len(entries))
	for index, entry := range entries {
		result[index] = tobymcp.SessionMount{
			Key:      entry.Key.String(),
			Profile:  entry.Profile,
			Target:   entry.Target,
			Access:   string(entry.Access),
			Optional: entry.Optional,
		}
	}
	sort.Slice(result, func(i, j int) bool {
		return result[i].Key < result[j].Key
	})

	return result
}

func nativeSnapshotBinds(
	binds []mount.Bind,
) []tobymcp.SessionBind {
	result := make([]tobymcp.SessionBind, len(binds))
	for index, bind := range binds {
		result[index] = tobymcp.SessionBind{
			Target:   bind.Target,
			Access:   string(bind.Access),
			Optional: bind.Optional,
		}
	}
	sort.Slice(result, func(i, j int) bool {
		return result[i].Target < result[j].Target
	})

	return result
}

func nativeSnapshotModels(
	endpoints []sessionconfig.ModelsEndpoint,
) []tobymcp.SessionModelsEndpoint {
	result := make([]tobymcp.SessionModelsEndpoint, 0, len(endpoints))
	for _, endpoint := range endpoints {
		models := make([]string, 0, len(endpoint.Models))
		for model := range endpoint.Models {
			models = append(models, model)
		}
		sort.Strings(models)
		if len(models) > tobymcp.MaxSnapshotCollectionItems {
			models = models[:tobymcp.MaxSnapshotCollectionItems]
		}

		result = append(result, tobymcp.SessionModelsEndpoint{
			Name:   endpoint.ID,
			Type:   endpoint.Type,
			Models: models,
		})
	}
	sort.Slice(result, func(i, j int) bool {
		return result[i].Name < result[j].Name
	})

	return result
}

func nativeSnapshotMCPs(config mcpconfig.Config) []tobymcp.SessionMCP {
	names := make([]string, 0, len(config.Servers))
	for name := range config.Servers {
		names = append(names, name)
	}
	sort.Strings(names)

	result := []tobymcp.SessionMCP{{
		Name:      "toby",
		Type:      "builtin",
		Enabled:   true,
		Status:    tobymcp.SessionMCPStatusRegistered,
		Transport: "stdio",
		Scope:     "run",
	}}
	for _, name := range names {
		server := config.Servers[name]
		scope := ""
		runtime := ""
		if server.Type == mcpconfig.ServerLocal {
			runtime = "bubblewrap"
			if effective, err := server.EffectiveScope(); err == nil {
				scope = string(effective)
			}
		}

		result = append(result, tobymcp.SessionMCP{
			Name:      name,
			Type:      string(server.Type),
			Enabled:   true,
			Status:    tobymcp.SessionMCPStatusConfigured,
			Runtime:   runtime,
			Transport: string(server.Transport),
			Scope:     scope,
			Network:   string(server.Network),
		})
	}

	return result
}

func sortedStrings(values []string) []string {
	result := append([]string(nil), values...)
	sort.Strings(result)
	return result
}
