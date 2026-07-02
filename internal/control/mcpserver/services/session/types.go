package sessionservice

// Data types for the session service: the tool input/output payloads and the
// introspection view structs the toby://session/* resources marshal to JSON.

// ResourcesReadInput selects which Toby resources to read. An empty URIs slice
// reads every available resource.
type ResourcesReadInput struct {
	URIs []string `json:"uris,omitempty" jsonschema:"toby:// resource URIs to read; omit to read every available resource"`
}

// ReadResourceContent is one resource's contents (or the error reading it).
type ReadResourceContent struct {
	URI      string `json:"uri" jsonschema:"the resource URI"`
	Title    string `json:"title,omitempty" jsonschema:"the resource title"`
	MIMEType string `json:"mimeType,omitempty" jsonschema:"the resource MIME type"`
	Text     string `json:"text,omitempty" jsonschema:"the resource contents"`
	Error    string `json:"error,omitempty" jsonschema:"why the resource could not be read"`
}

type ResourcesReadOutput struct {
	Resources []ReadResourceContent `json:"resources" jsonschema:"the requested resources, in request order"`
}

type RuntimeResourceOutput struct {
	Version string             `json:"version" jsonschema:"Toby version running this MCP server"`
	Debug   bool               `json:"debug" jsonschema:"whether Toby debug mode is enabled for this session"`
	Sandbox EnvironmentSandbox `json:"sandbox" jsonschema:"sandbox runtime and sandbox-visible paths"`
	Host    *EnvironmentHost   `json:"host,omitempty" jsonschema:"host paths; present only when debug mode is enabled"`
}

type ToolsResourceOutput struct {
	Tools     EnvironmentTools      `json:"tools" jsonschema:"available and active Toby tools"`
	Providers []EnvironmentProvider `json:"providers,omitempty" jsonschema:"configured providers without URLs or headers"`
}

type ProjectsResourceOutput struct {
	Projects []EnvironmentProject `json:"projects,omitempty" jsonschema:"project mounts visible in the sandbox"`
	Mounts   []EnvironmentMount   `json:"mounts,omitempty" jsonschema:"managed runtime and tool mounts"`
	Binds    []EnvironmentBind    `json:"binds,omitempty" jsonschema:"additional host bind mounts"`
}

type EnvironmentSandbox struct {
	Name        string            `json:"name,omitempty" jsonschema:"sandbox environment name"`
	Runtime     string            `json:"runtime,omitempty" jsonschema:"selected sandbox runtime"`
	RuntimeInfo map[string]any    `json:"runtimeInfo,omitempty" jsonschema:"runtime-defined introspection details"`
	Home        string            `json:"home,omitempty" jsonschema:"sandbox home path"`
	Workspace   string            `json:"workspace,omitempty" jsonschema:"sandbox project workspace path"`
	Root        string            `json:"root,omitempty" jsonschema:"sandbox runtime root path"`
	Bin         string            `json:"bin,omitempty" jsonschema:"Toby helper binary directory inside the sandbox"`
	Workdir     string            `json:"workdir,omitempty" jsonschema:"configured sandbox working directory"`
	Environment map[string]string `json:"environment,omitempty" jsonschema:"selected non-secret sandbox manager environment variables"`
}

type EnvironmentTools struct {
	Primary   string            `json:"primary,omitempty" jsonschema:"primary launched tool"`
	Active    []string          `json:"active,omitempty" jsonschema:"tools active in this launch"`
	Available []ToolSummary     `json:"available,omitempty" jsonschema:"registered Toby tools"`
	Groups    map[string]string `json:"groups,omitempty" jsonschema:"registered tool group by tool name"`
}

type ToolSummary struct {
	Name          string   `json:"name" jsonschema:"Toby tool name"`
	Command       string   `json:"command,omitempty" jsonschema:"CLI command used to launch the tool"`
	Launchable    bool     `json:"launchable" jsonschema:"whether this tool can be launched directly"`
	ContextGroups []string `json:"contextGroups,omitempty" jsonschema:"context groups this tool enables"`
}

type EnvironmentProject struct {
	Name        string `json:"name" jsonschema:"project mount name"`
	SandboxPath string `json:"sandboxPath" jsonschema:"path visible inside the sandbox"`
	HostPath    string `json:"hostPath,omitempty" jsonschema:"host project path; present only when debug mode is enabled"`
}

type EnvironmentMount struct {
	Key       string `json:"key" jsonschema:"managed mount key"`
	Profile   string `json:"profile,omitempty" jsonschema:"mount profile name"`
	Target    string `json:"target" jsonschema:"sandbox target path"`
	Access    string `json:"access,omitempty" jsonschema:"mount access mode"`
	Optional  bool   `json:"optional,omitempty" jsonschema:"whether the mount is optional"`
	Volume    string `json:"volume,omitempty" jsonschema:"Docker volume name; present only when debug mode is enabled"`
	SetupPath string `json:"setupPath,omitempty" jsonschema:"isolated setup path; present only when debug mode is enabled"`
}

type EnvironmentBind struct {
	Target   string `json:"target" jsonschema:"sandbox bind target"`
	Access   string `json:"access,omitempty" jsonschema:"bind access mode"`
	Optional bool   `json:"optional,omitempty" jsonschema:"whether the bind is optional"`
	HostPath string `json:"hostPath,omitempty" jsonschema:"host bind path; present only when debug mode is enabled"`
}

type EnvironmentProvider struct {
	Name   string   `json:"name" jsonschema:"provider config name"`
	Type   string   `json:"type,omitempty" jsonschema:"provider type"`
	Models []string `json:"models,omitempty" jsonschema:"configured model ids"`
}

type EnvironmentHost struct {
	Home          string `json:"home,omitempty" jsonschema:"host home path"`
	XDGConfigHome string `json:"xdgConfigHome,omitempty" jsonschema:"host XDG config home"`
	TobyConfigDir string `json:"tobyConfigDir,omitempty" jsonschema:"host Toby config directory"`
	ProjectRoot   string `json:"projectRoot,omitempty" jsonschema:"host project root"`
	SandboxRoot   string `json:"sandboxRoot,omitempty" jsonschema:"host sandbox root"`
}

type MCPStatusOutput struct {
	Servers []MCPStatusItem `json:"servers" jsonschema:"configured MCP servers"`
}

type MCPStatusItem struct {
	Name        string         `json:"name" jsonschema:"configured MCP name"`
	Type        string         `json:"type,omitempty" jsonschema:"local or remote"`
	Enabled     bool           `json:"enabled" jsonschema:"whether the MCP is enabled in config"`
	Status      string         `json:"status" jsonschema:"current Toby MCP sidecar/proxy status"`
	Runtime     string         `json:"runtime,omitempty" jsonschema:"local MCP runtime"`
	Transport   string         `json:"transport,omitempty" jsonschema:"local MCP transport"`
	PID         int            `json:"pid,omitempty" jsonschema:"local sidecar process id"`
	ExitCode    int            `json:"exitCode,omitempty" jsonschema:"last sidecar exit code"`
	UpdatedAt   string         `json:"updatedAt,omitempty" jsonschema:"status update timestamp"`
	RuntimeInfo map[string]any `json:"runtimeInfo,omitempty" jsonschema:"runtime-defined MCP sidecar introspection details"`
}

type MCPNameInput struct {
	Name string `json:"name" jsonschema:"configured MCP server name"`
}

type MCPActionOutput struct {
	Name   string        `json:"name" jsonschema:"configured MCP server name"`
	Action string        `json:"action" jsonschema:"lifecycle action requested"`
	Status MCPStatusItem `json:"status" jsonschema:"MCP status after the action was requested"`
}
