package tobymcp

// Defines the serializable, data-only session snapshot used by agent-hosted
// native MCP sessions. Its shape deliberately cannot carry upstream endpoints,
// headers, process arguments, environment values, credentials, or host paths.

// SessionSnapshot is the complete sandbox-safe introspection view captured for
// one native run.
type SessionSnapshot struct {
	Debug    bool                    `json:"debug"`
	Runtime  SessionRuntime          `json:"runtime"`
	Tools    SessionTools            `json:"tools"`
	Projects []SessionProject        `json:"projects,omitempty"`
	Mounts   []SessionMount          `json:"mounts,omitempty"`
	Binds    []SessionBind           `json:"binds,omitempty"`
	Models   []SessionModelsEndpoint `json:"models,omitempty"`
	MCPs     []SessionMCP            `json:"mcps,omitempty"`
}

// SessionRuntime describes the native sandbox runtime and only paths visible
// from inside the sandbox.
type SessionRuntime struct {
	Name         string `json:"name"`
	Profile      string `json:"profile"`
	Runtime      string `json:"runtime"`
	RootFSDigest string `json:"rootfsDigest,omitempty"`
	Network      string `json:"network,omitempty"`
	Home         string `json:"home"`
	Workspace    string `json:"workspace"`
	Root         string `json:"root"`
	Bin          string `json:"bin"`
	Workdir      string `json:"workdir"`
}

// SessionTools describes the primary, active, and available tool set without
// including executable names or launch arguments.
type SessionTools struct {
	Primary   string            `json:"primary,omitempty"`
	Active    []string          `json:"active,omitempty"`
	Available []SessionTool     `json:"available,omitempty"`
	Groups    map[string]string `json:"groups,omitempty"`
}

// SessionTool is one safe tool catalog entry.
type SessionTool struct {
	Name          string   `json:"name"`
	Launchable    bool     `json:"launchable"`
	ContextGroups []string `json:"contextGroups,omitempty"`
}

// SessionProject is one project path visible from inside the sandbox.
type SessionProject struct {
	Name        string `json:"name"`
	SandboxPath string `json:"sandboxPath"`
}

// SessionMount is one tool volume visible from inside the sandbox.
type SessionMount struct {
	Key      string `json:"key"`
	Profile  string `json:"profile"`
	Target   string `json:"target"`
	Access   string `json:"access"`
	Optional bool   `json:"optional,omitempty"`
}

// SessionBind is one external bind summarized without its host source.
type SessionBind struct {
	Target   string `json:"target"`
	Access   string `json:"access"`
	Optional bool   `json:"optional,omitempty"`
}

// SessionModelsEndpoint is one models endpoint summary without its URL,
// credential, or headers.
type SessionModelsEndpoint struct {
	Name   string   `json:"name"`
	Type   string   `json:"type,omitempty"`
	Models []string `json:"models,omitempty"`
}

// SessionMCPStatus is a closed, aggregate lifecycle state. It is never a
// diagnostic or error-message channel.
type SessionMCPStatus string

const (
	// SessionMCPStatusDisabled means the server is disabled by configuration.
	SessionMCPStatusDisabled SessionMCPStatus = "disabled"
	// SessionMCPStatusConfigured means the server is configured but not registered.
	SessionMCPStatusConfigured SessionMCPStatus = "configured"
	// SessionMCPStatusRegistered means the agent knows how to start the server.
	SessionMCPStatusRegistered SessionMCPStatus = "registered"
	// SessionMCPStatusCold means the resource has not started.
	SessionMCPStatusCold SessionMCPStatus = "cold"
	// SessionMCPStatusStarting means the resource is starting.
	SessionMCPStatusStarting SessionMCPStatus = "starting"
	// SessionMCPStatusReady means the resource can accept requests.
	SessionMCPStatusReady SessionMCPStatus = "ready"
	// SessionMCPStatusRunning means the resource is running.
	SessionMCPStatusRunning SessionMCPStatus = "running"
	// SessionMCPStatusIdle means the resource has no active users.
	SessionMCPStatusIdle SessionMCPStatus = "idle"
	// SessionMCPStatusStopping means the resource is stopping.
	SessionMCPStatusStopping SessionMCPStatus = "stopping"
	// SessionMCPStatusExited means the resource process exited.
	SessionMCPStatusExited SessionMCPStatus = "exited"
	// SessionMCPStatusFailed means the resource failed.
	SessionMCPStatusFailed SessionMCPStatus = "failed"
	// SessionMCPStatusStopped means the resource was stopped.
	SessionMCPStatusStopped SessionMCPStatus = "stopped"
	// SessionMCPStatusUnregistered means the resource is unknown to the agent.
	SessionMCPStatusUnregistered SessionMCPStatus = "unregistered"
	// SessionMCPStatusUnknown means no more specific state is available.
	SessionMCPStatusUnknown SessionMCPStatus = "unknown"
)

// SessionMCP is one configured or active MCP summary without its endpoint,
// headers, image, environment, or process command.
type SessionMCP struct {
	Name      string           `json:"name"`
	Type      string           `json:"type"`
	Enabled   bool             `json:"enabled"`
	Status    SessionMCPStatus `json:"status"`
	Runtime   string           `json:"runtime,omitempty"`
	Transport string           `json:"transport"`
	Scope     string           `json:"scope,omitempty"`
	Network   string           `json:"network,omitempty"`
}

// Clone returns a deeply detached copy of the snapshot.
func (s SessionSnapshot) Clone() SessionSnapshot {
	clone := s
	clone.Tools = s.Tools.clone()
	clone.Projects = append([]SessionProject(nil), s.Projects...)
	clone.Mounts = append([]SessionMount(nil), s.Mounts...)
	clone.Binds = append([]SessionBind(nil), s.Binds...)
	if s.Models != nil {
		clone.Models = make([]SessionModelsEndpoint, len(s.Models))
		for index, endpoint := range s.Models {
			clone.Models[index] = endpoint
			clone.Models[index].Models = append(
				[]string(nil),
				endpoint.Models...,
			)
		}
	}
	clone.MCPs = append([]SessionMCP(nil), s.MCPs...)

	return clone
}

func (s SessionTools) clone() SessionTools {
	clone := s
	clone.Active = append([]string(nil), s.Active...)
	if s.Available != nil {
		clone.Available = make([]SessionTool, len(s.Available))
		for index, tool := range s.Available {
			clone.Available[index] = tool
			clone.Available[index].ContextGroups = append(
				[]string(nil),
				tool.ContextGroups...,
			)
		}
	}
	if s.Groups != nil {
		clone.Groups = make(map[string]string, len(s.Groups))
		for name, group := range s.Groups {
			clone.Groups[name] = group
		}
	}

	return clone
}
