package sessionservice

// Renders the strictly validated native snapshot into MCP resource payloads.

import "petris.dev/toby/internal/tobymcp"

type stateView struct {
	tobymcp.SessionSnapshot
}

func (s stateView) environmentSandbox() EnvironmentSandbox {
	runtime := s.Runtime
	return EnvironmentSandbox{
		Name:         runtime.Name,
		Profile:      runtime.Profile,
		Runtime:      runtime.Runtime,
		RootFSDigest: runtime.RootFSDigest,
		Network:      runtime.Network,
		Home:         runtime.Home,
		Workspace:    runtime.Workspace,
		Root:         runtime.Root,
		Bin:          runtime.Bin,
		Workdir:      runtime.Workdir,
	}
}

func (s stateView) environmentTools() EnvironmentTools {
	tools := s.Tools
	result := EnvironmentTools{
		Primary: tools.Primary,
		Active:  append([]string(nil), tools.Active...),
	}
	if tools.Groups != nil {
		result.Groups = make(map[string]string, len(tools.Groups))
		for name, group := range tools.Groups {
			result.Groups[name] = group
		}
	}
	if tools.Available != nil {
		result.Available = make([]ToolSummary, len(tools.Available))
		for index, tool := range tools.Available {
			result.Available[index] = ToolSummary{
				Name:       tool.Name,
				Launchable: tool.Launchable,
				ContextGroups: append(
					[]string(nil),
					tool.ContextGroups...,
				),
			}
		}
	}

	return result
}

func (s stateView) environmentProjects() []EnvironmentProject {
	if s.Projects == nil {
		return nil
	}

	result := make([]EnvironmentProject, len(s.Projects))
	for index, project := range s.Projects {
		result[index] = EnvironmentProject{
			Name:        project.Name,
			SandboxPath: project.SandboxPath,
		}
	}

	return result
}

func (s stateView) environmentMounts() []EnvironmentMount {
	if s.Mounts == nil {
		return nil
	}

	result := make([]EnvironmentMount, len(s.Mounts))
	for index, mount := range s.Mounts {
		result[index] = EnvironmentMount{
			Key:      mount.Key,
			Profile:  mount.Profile,
			Target:   mount.Target,
			Access:   mount.Access,
			Optional: mount.Optional,
		}
	}

	return result
}

func (s stateView) environmentBinds() []EnvironmentBind {
	if s.Binds == nil {
		return nil
	}

	result := make([]EnvironmentBind, len(s.Binds))
	for index, bind := range s.Binds {
		result[index] = EnvironmentBind{
			Target:   bind.Target,
			Access:   bind.Access,
			Optional: bind.Optional,
		}
	}

	return result
}

func (s stateView) environmentModels() []EnvironmentModelsEndpoint {
	if s.Models == nil {
		return nil
	}

	result := make([]EnvironmentModelsEndpoint, len(s.Models))
	for index, endpoint := range s.Models {
		result[index] = EnvironmentModelsEndpoint{
			Name:   endpoint.Name,
			Type:   endpoint.Type,
			Models: append([]string(nil), endpoint.Models...),
		}
	}

	return result
}

func (s stateView) mcpStatusItems() []MCPStatusItem {
	if s.MCPs == nil {
		return nil
	}

	result := make([]MCPStatusItem, len(s.MCPs))
	for index, server := range s.MCPs {
		result[index] = MCPStatusItem{
			Name:      server.Name,
			Type:      server.Type,
			Enabled:   server.Enabled,
			Status:    string(server.Status),
			Runtime:   server.Runtime,
			Transport: server.Transport,
			Scope:     server.Scope,
			Network:   server.Network,
		}
	}

	return result
}
