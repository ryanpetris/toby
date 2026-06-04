package mcpserver

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"sort"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"go.uber.org/fx"
	"petris.dev/toby/container/layout"
	"petris.dev/toby/internal/config"
	"petris.dev/toby/internal/config/toby"
	"petris.dev/toby/internal/control/mcpproxy"
	"petris.dev/toby/internal/sandbox"
	"petris.dev/toby/internal/tools/tool"
	"petris.dev/toby/internal/version"
)

const mcpStartDescription = "Start a configured local Toby-managed MCP sidecar."

const mcpStopDescription = "Stop a configured local Toby-managed MCP sidecar."

const mcpRestartDescription = "Restart a configured local Toby-managed MCP sidecar."

const resourcesReadDescription = "Read one or more Toby resources by URI (for example toby://session/runtime or toby://docs/introspection). Pass uris to select specific resources; omit uris to read every available resource. Use this when the MCP client cannot read MCP resources directly."

type TobyServiceResult struct {
	fx.Out

	Service Service `group:"toby.sandbox.mcp.services"`
}

type TobyService struct{}

func NewTobyService() TobyServiceResult {
	return TobyServiceResult{Service: TobyService{}}
}

func (TobyService) Tools() []Tool {
	return []Tool{
		{Name: "mcp.start", Register: func(server *mcp.Server, toby *Server) {
			mcp.AddTool(server, &mcp.Tool{Name: "mcp.start", Description: mcpStartDescription}, toby.mcpStart)
		}},
		{Name: "mcp.stop", Register: func(server *mcp.Server, toby *Server) {
			mcp.AddTool(server, &mcp.Tool{Name: "mcp.stop", Description: mcpStopDescription}, toby.mcpStop)
		}},
		{Name: "mcp.restart", Register: func(server *mcp.Server, toby *Server) {
			mcp.AddTool(server, &mcp.Tool{Name: "mcp.restart", Description: mcpRestartDescription}, toby.mcpRestart)
		}},
		{Name: "resources.read", Register: func(server *mcp.Server, toby *Server) {
			mcp.AddTool(server, &mcp.Tool{Name: "resources.read", Description: resourcesReadDescription}, toby.resourcesRead)
		}},
	}
}

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

// resourcesRead returns the contents of the named toby:// resources, mirroring
// the MCP resources/read path for clients that do not surface resources as
// readable. Unknown or failing URIs are reported per item.
func (s *Server) resourcesRead(ctx context.Context, _ *mcp.CallToolRequest, input ResourcesReadInput) (*mcp.CallToolResult, ResourcesReadOutput, error) {
	byURI := make(map[string]Resource, len(s.resources))
	all := make([]string, 0, len(s.resources))
	for _, resource := range s.resources {
		byURI[resource.URI] = resource
		all = append(all, resource.URI)
	}
	wanted := input.URIs
	if len(wanted) == 0 {
		wanted = all
	}
	out := ResourcesReadOutput{Resources: make([]ReadResourceContent, 0, len(wanted))}
	failed := false
	for _, uri := range wanted {
		resource, ok := byURI[uri]
		if !ok {
			failed = true
			out.Resources = append(out.Resources, ReadResourceContent{URI: uri, Error: "unknown resource"})
			continue
		}
		text, err := resource.Read(ctx, s)
		if err != nil {
			failed = true
			out.Resources = append(out.Resources, ReadResourceContent{URI: uri, Error: err.Error()})
			continue
		}
		mimeType := resource.MIMEType
		if mimeType == "" {
			mimeType = "text/markdown; charset=utf-8"
		}
		out.Resources = append(out.Resources, ReadResourceContent{URI: uri, Title: resource.Title, MIMEType: mimeType, Text: text})
	}
	var result *mcp.CallToolResult
	if failed {
		result = &mcp.CallToolResult{IsError: true}
	}
	return result, out, nil
}

func (TobyService) Resources() []Resource {
	return []Resource{
		{
			URI:         "toby://docs/mcps",
			Name:        "toby.docs.mcps",
			Title:       "Toby-Managed MCPs",
			Description: "Guidance for Toby-managed MCP proxying and lifecycle tools.",
			FS:          resourceDocs,
			FilePath:    "resources/mcps.md",
		},
		{
			URI:         "toby://docs/introspection",
			Name:        "toby.docs.introspection",
			Title:       "Toby Introspection",
			Description: "Guidance for Toby session introspection resources and redaction behavior.",
			FS:          resourceDocs,
			FilePath:    "resources/introspection.md",
		},
		{
			URI:         "toby://session/runtime",
			Name:        "toby.session.runtime",
			Title:       "Toby Session Runtime",
			Description: "Current Toby version, debug mode, sandbox runtime, and runtime paths.",
			Text:        func(ctx context.Context, toby *Server) (string, error) { return toby.runtimeResource(ctx) },
		},
		{
			URI:         "toby://session/mcps",
			Name:        "toby.session.mcps",
			Title:       "Toby Session MCPs",
			Description: "Configured MCP status and redacted runtime details for this session.",
			Text:        func(ctx context.Context, toby *Server) (string, error) { return toby.mcpsResource(ctx) },
		},
		{
			URI:         "toby://session/tools",
			Name:        "toby.session.tools",
			Title:       "Toby Session Tools",
			Description: "Active and available Toby tools plus provider summaries for this session.",
			Text:        func(ctx context.Context, toby *Server) (string, error) { return toby.toolsResource(ctx) },
		},
		{
			URI:         "toby://session/projects",
			Name:        "toby.session.projects",
			Title:       "Toby Session Projects",
			Description: "Visible projects, additional binds, and managed mounts for this session.",
			Text:        func(ctx context.Context, toby *Server) (string, error) { return toby.projectsResource(ctx) },
		},
	}
}

type SessionState struct {
	Debug       bool
	Paths       config.Paths
	Options     tool.CommandOptions
	Sandbox     *sandbox.SandboxService
	MCPProxy    *mcpproxy.Service
	Config      *tobyconfig.Service
	Registry    *tool.Registry
	ActiveTools []string
	PrimaryTool string
}

func (s SessionState) Clone() SessionState {
	s.ActiveTools = append([]string(nil), s.ActiveTools...)
	if s.Options.Debug != nil {
		debug := *s.Options.Debug
		s.Options.Debug = &debug
	}
	return s
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
	Context     string            `json:"context,omitempty" jsonschema:"generated Toby context path inside the sandbox"`
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

func (s *Server) runtimeResource(context.Context) (string, error) {
	state := s.state
	return markdownJSONResource("Toby Session Runtime", "Current Toby version, debug mode, sandbox runtime, and runtime paths for this session.", RuntimeResourceOutput{Version: version.String(), Debug: state.Debug, Sandbox: state.environmentSandbox(), Host: state.environmentHost()})
}

func (s *Server) mcpsResource(context.Context) (string, error) {
	return markdownJSONResource("Toby Session MCPs", "Configured MCP status for this session. URLs, headers, commands, argv, and environment values are redacted.", MCPStatusOutput{Servers: s.state.mcpStatusItems()})
}

func (s *Server) toolsResource(context.Context) (string, error) {
	state := s.state
	return markdownJSONResource("Toby Session Tools", "Active and available Toby tools plus provider summaries for this session.", ToolsResourceOutput{Tools: state.environmentTools(), Providers: state.environmentProviders()})
}

func (s *Server) projectsResource(context.Context) (string, error) {
	state := s.state
	return markdownJSONResource("Toby Session Projects", "Visible projects, additional binds, and managed mounts for this session.", ProjectsResourceOutput{Projects: state.environmentProjects(), Mounts: state.environmentMounts(), Binds: state.environmentBinds()})
}

func markdownJSONResource(title, description string, value any) (string, error) {
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("# %s\n\n%s\n\n```json\n%s\n```\n", title, description, data), nil
}

func (s *Server) mcpStart(ctx context.Context, _ *mcp.CallToolRequest, input MCPNameInput) (*mcp.CallToolResult, MCPActionOutput, error) {
	return s.mcpLifecycle(ctx, "start", input.Name)
}

func (s *Server) mcpStop(ctx context.Context, _ *mcp.CallToolRequest, input MCPNameInput) (*mcp.CallToolResult, MCPActionOutput, error) {
	return s.mcpLifecycle(ctx, "stop", input.Name)
}

func (s *Server) mcpRestart(ctx context.Context, _ *mcp.CallToolRequest, input MCPNameInput) (*mcp.CallToolResult, MCPActionOutput, error) {
	return s.mcpLifecycle(ctx, "restart", input.Name)
}

func (s *Server) mcpLifecycle(ctx context.Context, action, name string) (*mcp.CallToolResult, MCPActionOutput, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return nil, MCPActionOutput{}, fmt.Errorf("mcp name is required")
	}
	proxy := s.state.MCPProxy
	if proxy == nil {
		return nil, MCPActionOutput{}, fmt.Errorf("mcp proxy service is not configured")
	}
	var err error
	switch action {
	case "start":
		err = proxy.Start(ctx, name)
	case "stop":
		err = proxy.Stop(ctx, name)
	case "restart":
		err = proxy.Restart(ctx, name)
	default:
		err = fmt.Errorf("unsupported mcp action %q", action)
	}
	if err != nil {
		return nil, MCPActionOutput{}, err
	}
	return nil, MCPActionOutput{Name: name, Action: action, Status: s.state.mcpStatusItem(name)}, nil
}

func (s SessionState) environmentSandbox() EnvironmentSandbox {
	runtime := sandbox.RuntimeInfo{}
	if s.Sandbox != nil {
		runtime = s.Sandbox.RuntimeInfo(s.Debug)
	}
	info := EnvironmentSandbox{Name: s.Options.Env, Runtime: runtime.Runtime, RuntimeInfo: sanitizeRuntimeInfo(runtime.Info), Home: layout.Home, Workspace: layout.Workspace, Root: layout.Root, Context: layout.Context, Bin: layout.Bin, Workdir: s.Options.Workdir, Environment: map[string]string{}}
	if s.Sandbox != nil {
		for _, name := range []string{"HOME"} {
			if value, ok := s.Sandbox.GetEnvironment(name); ok {
				info.Environment[name] = value
			}
		}
	}
	if len(info.Environment) == 0 {
		info.Environment = nil
	}
	return info
}

func (s SessionState) environmentTools() EnvironmentTools {
	tools := EnvironmentTools{Primary: s.PrimaryTool, Active: append([]string(nil), s.ActiveTools...), Groups: map[string]string{}}
	if s.Registry == nil {
		return tools
	}
	for group, names := range tool.ToolGroups {
		for _, name := range names {
			tools.Groups[name] = group
		}
	}
	for _, name := range s.Registry.ToolNames() {
		item, ok := s.Registry.Get(name)
		if !ok {
			continue
		}
		tools.Available = append(tools.Available, ToolSummary{Name: item.Name(), Command: item.CommandName(), Launchable: item.LaunchHelp() != "", ContextGroups: item.ContextGroups()})
	}
	return tools
}

func (s SessionState) environmentProjects() []EnvironmentProject {
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

func (s SessionState) environmentMounts() []EnvironmentMount {
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

func (s SessionState) environmentBinds() []EnvironmentBind {
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

func (s SessionState) environmentProviders() []EnvironmentProvider {
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

func (s SessionState) environmentHost() *EnvironmentHost {
	if !s.Debug {
		return nil
	}
	return &EnvironmentHost{Home: s.Paths.Home, XDGConfigHome: s.Paths.XDGConfigHome, TobyConfigDir: s.Paths.TobyConfigDir(), ProjectRoot: s.Paths.ProjectRoot, SandboxRoot: s.Paths.SandboxRoot}
}

func (s SessionState) mcpStatusItems() []MCPStatusItem {
	servers := map[string]tobyconfig.MCPServer{}
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
			items = append(items, s.mcpStatusItemFromSnapshot(status, tobyconfig.MCPServer{}, true))
		}
	}
	return items
}

func (s SessionState) mcpStatusItem(name string) MCPStatusItem {
	if s.Config != nil {
		if server, ok := s.Config.MCPServers()[name]; ok {
			return s.mcpStatusItemForServer(name, server)
		}
	}
	if s.MCPProxy != nil {
		for _, status := range s.MCPProxy.Status() {
			if status.Name == name {
				return s.mcpStatusItemFromSnapshot(status, tobyconfig.MCPServer{}, true)
			}
		}
	}
	return MCPStatusItem{Name: name, Status: "unknown"}
}

func (s SessionState) mcpStatusItemForServer(name string, server tobyconfig.MCPServer) MCPStatusItem {
	if !server.Enabled() {
		return MCPStatusItem{Name: name, Type: server.Type(), Enabled: false, Status: "disabled", Runtime: server.Runtime().Type, Transport: server.Transport()}
	}
	if s.MCPProxy != nil {
		for _, status := range s.MCPProxy.Status() {
			if status.Name == name {
				return s.mcpStatusItemFromSnapshot(status, server, true)
			}
		}
	}
	return MCPStatusItem{Name: name, Type: server.Type(), Enabled: true, Status: "unregistered", Runtime: server.Runtime().Type, Transport: server.Transport()}
}

func (s SessionState) mcpStatusItemFromSnapshot(status mcpproxy.StatusSnapshot, server tobyconfig.MCPServer, enabled bool) MCPStatusItem {
	item := MCPStatusItem{Name: status.Name, Type: server.Type(), Enabled: enabled, Status: string(status.Status), Runtime: string(status.Runtime), Transport: string(status.Transport), PID: status.PID, ExitCode: status.ExitCode}
	if item.Type == "" && status.Runtime != "" {
		item.Type = tobyconfig.MCPTypeLocal
	}
	if item.Type == "" {
		item.Type = server.Type()
	}
	if item.Runtime == "" {
		item.Runtime = server.Runtime().Type
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

func sanitizeRuntimeInfo(info map[string]any) map[string]any {
	if len(info) == 0 {
		return nil
	}
	clean := map[string]any{}
	for key, value := range info {
		if unsafeRuntimeInfoKey(key) {
			continue
		}
		if sanitized, ok := sanitizeRuntimeInfoValue(value); ok {
			clean[key] = sanitized
		}
	}
	if len(clean) == 0 {
		return nil
	}
	return clean
}

func sanitizeRuntimeInfoValue(value any) (any, bool) {
	return sanitizeRuntimeInfoReflect(reflect.ValueOf(value))
}

func sanitizeRuntimeInfoReflect(value reflect.Value) (any, bool) {
	if !value.IsValid() {
		return nil, true
	}
	for value.Kind() == reflect.Interface || value.Kind() == reflect.Pointer {
		if value.IsNil() {
			return nil, true
		}
		value = value.Elem()
	}
	switch value.Kind() {
	case reflect.Map:
		if value.Type().Key().Kind() != reflect.String {
			return nil, false
		}
		clean := map[string]any{}
		iter := value.MapRange()
		for iter.Next() {
			key := iter.Key().String()
			if unsafeRuntimeInfoKey(key) {
				continue
			}
			if sanitized, ok := sanitizeRuntimeInfoReflect(iter.Value()); ok {
				clean[key] = sanitized
			}
		}
		return clean, len(clean) > 0
	case reflect.Slice, reflect.Array:
		clean := make([]any, 0, value.Len())
		for i := 0; i < value.Len(); i++ {
			if sanitized, ok := sanitizeRuntimeInfoReflect(value.Index(i)); ok {
				clean = append(clean, sanitized)
			}
		}
		return clean, len(clean) > 0
	case reflect.Bool:
		return value.Bool(), true
	case reflect.String:
		return value.String(), true
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		return value.Int(), true
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Uintptr:
		return value.Uint(), true
	case reflect.Float32, reflect.Float64:
		return value.Float(), true
	default:
		return nil, false
	}
}

func unsafeRuntimeInfoKey(key string) bool {
	key = strings.ToLower(strings.TrimSpace(key))
	for _, unsafe := range []string{"url", "header", "headers", "command", "argv", "env", "environment"} {
		if key == unsafe || strings.Contains(key, unsafe) {
			return true
		}
	}
	return false
}

func sortedMapKeys[V any](items map[string]V) []string {
	names := make([]string, 0, len(items))
	for name := range items {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}
