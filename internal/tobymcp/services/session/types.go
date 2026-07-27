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

// ResourcesReadOutput contains the results of a batch resource read.
type ResourcesReadOutput struct {
	Resources []ReadResourceContent `json:"resources" jsonschema:"the requested resources, in request order"`
}

// RuntimeResourceOutput describes the current Toby and sandbox runtime.
type RuntimeResourceOutput struct {
	Version string             `json:"version" jsonschema:"Toby version running this MCP server"`
	Debug   bool               `json:"debug" jsonschema:"whether Toby debug mode is enabled for this session"`
	Sandbox EnvironmentSandbox `json:"sandbox" jsonschema:"sandbox runtime and sandbox-visible paths"`
}

// ToolsResourceOutput describes available tools and models endpoints.
type ToolsResourceOutput struct {
	Tools  EnvironmentTools            `json:"tools" jsonschema:"available and active Toby tools"`
	Models []EnvironmentModelsEndpoint `json:"models,omitempty" jsonschema:"configured models endpoints without URLs, credentials, or headers"`
}

// ProjectsResourceOutput describes visible projects, managed mounts, and binds.
type ProjectsResourceOutput struct {
	Projects []EnvironmentProject `json:"projects,omitempty" jsonschema:"project mounts visible in the sandbox"`
	Mounts   []EnvironmentMount   `json:"mounts,omitempty" jsonschema:"managed runtime and tool mounts"`
	Binds    []EnvironmentBind    `json:"binds,omitempty" jsonschema:"additional host bind mounts"`
}

// EnvironmentSandbox describes the current sandbox runtime and layout.
type EnvironmentSandbox struct {
	Name         string `json:"name" jsonschema:"sandbox environment name"`
	Profile      string `json:"profile" jsonschema:"persistent home and default tool-volume profile"`
	Runtime      string `json:"runtime" jsonschema:"selected sandbox runtime"`
	RootFSDigest string `json:"rootfsDigest,omitempty" jsonschema:"immutable native rootfs digest"`
	Network      string `json:"network,omitempty" jsonschema:"selected sandbox network policy"`
	Home         string `json:"home" jsonschema:"sandbox home path"`
	Workspace    string `json:"workspace" jsonschema:"sandbox project workspace path"`
	Root         string `json:"root" jsonschema:"sandbox runtime root path"`
	Bin          string `json:"bin" jsonschema:"Toby helper binary directory inside the sandbox"`
	Workdir      string `json:"workdir" jsonschema:"configured sandbox working directory"`
}

// EnvironmentTools describes active and available tools.
type EnvironmentTools struct {
	Primary   string            `json:"primary,omitempty" jsonschema:"primary launched tool"`
	Active    []string          `json:"active,omitempty" jsonschema:"tools active in this launch"`
	Available []ToolSummary     `json:"available,omitempty" jsonschema:"registered Toby tools"`
	Groups    map[string]string `json:"groups,omitempty" jsonschema:"registered tool group by tool name"`
}

// ToolSummary describes one registered tool.
type ToolSummary struct {
	Name          string   `json:"name" jsonschema:"Toby tool name"`
	Launchable    bool     `json:"launchable" jsonschema:"whether this tool can be launched directly"`
	ContextGroups []string `json:"contextGroups,omitempty" jsonschema:"context groups this tool enables"`
}

// EnvironmentProject describes one visible project.
type EnvironmentProject struct {
	Name        string `json:"name" jsonschema:"project mount name"`
	SandboxPath string `json:"sandboxPath" jsonschema:"path visible inside the sandbox"`
}

// EnvironmentMount describes one managed sandbox mount.
type EnvironmentMount struct {
	Key      string `json:"key" jsonschema:"managed mount key"`
	Profile  string `json:"profile" jsonschema:"global tool-volume profile"`
	Target   string `json:"target" jsonschema:"sandbox target path"`
	Access   string `json:"access,omitempty" jsonschema:"mount access mode"`
	Optional bool   `json:"optional,omitempty" jsonschema:"whether the mount is optional"`
}

// EnvironmentBind describes one additional host bind.
type EnvironmentBind struct {
	Target   string `json:"target" jsonschema:"sandbox bind target"`
	Access   string `json:"access,omitempty" jsonschema:"bind access mode"`
	Optional bool   `json:"optional,omitempty" jsonschema:"whether the bind is optional"`
}

// EnvironmentModelsEndpoint describes one configured models endpoint without secrets.
type EnvironmentModelsEndpoint struct {
	Name   string   `json:"name" jsonschema:"models endpoint config name"`
	Type   string   `json:"type,omitempty" jsonschema:"models protocol type"`
	Models []string `json:"models,omitempty" jsonschema:"discovered model ids"`
}

// MCPStatusOutput contains configured MCP server statuses.
type MCPStatusOutput struct {
	Servers []MCPStatusItem `json:"servers" jsonschema:"configured MCP servers"`
}

// MCPStatusItem describes one configured or registered MCP server.
type MCPStatusItem struct {
	Name      string `json:"name" jsonschema:"configured MCP name"`
	Type      string `json:"type,omitempty" jsonschema:"builtin, local, or remote"`
	Enabled   bool   `json:"enabled" jsonschema:"whether the MCP is enabled for this run"`
	Status    string `json:"status" jsonschema:"bounded agent-managed MCP status"`
	Runtime   string `json:"runtime,omitempty" jsonschema:"local MCP runtime"`
	Transport string `json:"transport,omitempty" jsonschema:"MCP connector transport"`
	Scope     string `json:"scope,omitempty" jsonschema:"effective local MCP sharing scope"`
	Network   string `json:"network,omitempty" jsonschema:"local MCP network policy"`
}
