package sessionservice

// stateView adds the introspection builders to the session's non-secret state:
// it renders the sandbox/tools/projects/providers/host views and the per-server
// MCP status items that the toby://session/* resources expose.

import (
	"sort"

	appconfig "petris.dev/toby/config/app"
	"petris.dev/toby/container/layout"
	"petris.dev/toby/control/mcpproxy"
	"petris.dev/toby/control/mcpserver"
	sandbox "petris.dev/toby/sandbox/runtime"
)

// stateView adds the introspection builders to the session's non-secret state.
// Embedding promotes SessionState's exported fields, so the builder bodies read
// them directly.
type stateView struct {
	mcpserver.SessionState
}

func (s stateView) environmentSandbox() EnvironmentSandbox {
	runtime := sandbox.RuntimeInfo{}
	if s.Sandbox != nil {
		runtime = s.Sandbox.RuntimeInfo(s.Debug)
	}
	info := EnvironmentSandbox{Name: s.Options.Env, Runtime: runtime.Runtime, RuntimeInfo: sanitizeRuntimeInfo(runtime.Info), Home: layout.Home, Workspace: layout.Workspace, Root: layout.Root, Context: layout.Context, Bin: layout.Bin, Workdir: s.Options.Workdir, Environment: map[string]string{}}
	if s.Sandbox != nil {
		for _, name := range []string{"HOME"} {
			if value, ok := s.Sandbox.Environment(name); ok {
				info.Environment[name] = value
			}
		}
	}
	if len(info.Environment) == 0 {
		info.Environment = nil
	}
	return info
}

func (s stateView) environmentTools() EnvironmentTools {
	envTools := EnvironmentTools{Primary: s.PrimaryTool, Active: append([]string(nil), s.ActiveTools...), Groups: map[string]string{}}
	if s.Registry == nil {
		return envTools
	}
	for _, name := range s.Registry.ToolNames() {
		item, ok := s.Registry.Get(name)
		if !ok {
			continue
		}
		if group := item.Group(); group != "" {
			envTools.Groups[name] = group
		}
		envTools.Available = append(envTools.Available, ToolSummary{Name: item.Name(), Command: item.CommandName(), Launchable: item.LaunchHelp() != "", ContextGroups: item.ContextGroups()})
	}
	return envTools
}

func (s stateView) environmentProjects() []EnvironmentProject {
	if s.Sandbox == nil {
		return nil
	}
	projects := s.Sandbox.ProjectMounts()
	result := make([]EnvironmentProject, 0, len(projects))
	for _, project := range projects {
		item := EnvironmentProject{Name: project.Name, SandboxPath: project.SandboxPath}
		if s.Debug {
			item.HostPath = project.HostPath
		}
		result = append(result, item)
	}
	return result
}

func (s stateView) environmentMounts() []EnvironmentMount {
	if s.Sandbox == nil {
		return nil
	}
	mounts := s.Sandbox.MountInfos()
	result := make([]EnvironmentMount, 0, len(mounts))
	for _, m := range mounts {
		item := EnvironmentMount{Key: m.Key.String(), Profile: m.Profile, Target: m.Target, Access: string(m.Access), Optional: m.Optional}
		if s.Debug {
			item.Volume = m.Volume
			item.SetupPath = m.SetupPath
		}
		result = append(result, item)
	}
	return result
}

func (s stateView) environmentBinds() []EnvironmentBind {
	if s.Sandbox == nil {
		return nil
	}
	binds := s.Sandbox.StartBindSnapshot()
	result := make([]EnvironmentBind, 0, len(binds))
	for _, bind := range binds {
		item := EnvironmentBind{Target: bind.Target, Access: string(bind.Access), Optional: bind.Optional}
		if s.Debug {
			item.HostPath = bind.HostPath
		}
		result = append(result, item)
	}
	return result
}

func (s stateView) environmentProviders() []EnvironmentProvider {
	if s.Config == nil {
		return nil
	}
	providers := s.Config.Providers()
	names := sortedMapKeys(providers)
	result := make([]EnvironmentProvider, 0, len(names))
	for _, name := range names {
		provider := providers[name]
		models := sortedMapKeys(provider.Models)
		result = append(result, EnvironmentProvider{Name: name, Type: provider.Type, Models: models})
	}
	return result
}

func (s stateView) environmentHost() *EnvironmentHost {
	if !s.Debug {
		return nil
	}
	return &EnvironmentHost{Home: s.Paths.Home, XDGConfigHome: s.Paths.XDGConfigHome, TobyConfigDir: s.Paths.TobyConfigDir(), ProjectRoot: s.Paths.ProjectRoot, SandboxRoot: s.Paths.SandboxRoot}
}

func (s stateView) mcpStatusItems() []MCPStatusItem {
	servers := map[string]appconfig.MCPServer{}
	if s.Config != nil {
		servers = s.Config.MCPServers()
	}
	names := sortedMapKeys(servers)
	seen := map[string]bool{}
	items := make([]MCPStatusItem, 0, len(names))
	for _, name := range names {
		items = append(items, s.mcpStatusItemForServer(name, servers[name]))
		seen[name] = true
	}
	if s.MCPProxy != nil {
		for _, status := range s.MCPProxy.Status() {
			if seen[status.Name] {
				continue
			}
			items = append(items, s.mcpStatusItemFromSnapshot(status, appconfig.MCPServer{}, true))
		}
	}
	return items
}

func (s stateView) mcpStatusItem(name string) MCPStatusItem {
	if s.Config != nil {
		if server, ok := s.Config.MCPServers()[name]; ok {
			return s.mcpStatusItemForServer(name, server)
		}
	}
	if s.MCPProxy != nil {
		for _, status := range s.MCPProxy.Status() {
			if status.Name == name {
				return s.mcpStatusItemFromSnapshot(status, appconfig.MCPServer{}, true)
			}
		}
	}
	return MCPStatusItem{Name: name, Status: "unknown"}
}

// mcpServerRuntime reports the sidecar runtime a server would use: local servers
// run in the docker sidecar; remote servers have none.
func mcpServerRuntime(server appconfig.MCPServer) string {
	if server.Local() {
		return "docker"
	}
	return ""
}

func (s stateView) mcpStatusItemForServer(name string, server appconfig.MCPServer) MCPStatusItem {
	if !server.Enabled() {
		return MCPStatusItem{Name: name, Type: server.Type(), Enabled: false, Status: "disabled", Runtime: mcpServerRuntime(server), Transport: server.Transport()}
	}
	if s.MCPProxy != nil {
		for _, status := range s.MCPProxy.Status() {
			if status.Name == name {
				return s.mcpStatusItemFromSnapshot(status, server, true)
			}
		}
	}
	return MCPStatusItem{Name: name, Type: server.Type(), Enabled: true, Status: "unregistered", Runtime: mcpServerRuntime(server), Transport: server.Transport()}
}

func (s stateView) mcpStatusItemFromSnapshot(status mcpproxy.StatusSnapshot, server appconfig.MCPServer, enabled bool) MCPStatusItem {
	runtime := mcpServerRuntime(server)
	item := MCPStatusItem{Name: status.Name, Type: server.Type(), Enabled: enabled, Status: string(status.Status), Runtime: runtime, Transport: string(status.Transport), PID: status.PID, ExitCode: status.ExitCode}
	if item.Type == "" && runtime != "" {
		item.Type = appconfig.MCPTypeLocal
	}
	if item.Type == "" {
		item.Type = server.Type()
	}
	if item.Transport == "" {
		item.Transport = server.Transport()
	}
	if !status.UpdatedAt.IsZero() {
		item.UpdatedAt = status.UpdatedAt.Format("2006-01-02T15:04:05Z07:00")
	}
	if s.Debug {
		item.RuntimeInfo = sanitizeRuntimeInfo(status.RuntimeInfo)
	}
	return item
}

func sortedMapKeys[V any](items map[string]V) []string {
	names := make([]string, 0, len(items))
	for name := range items {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}
