package mcpproxy

// Wire and value types shared across the proxy: transport/status enums, the
// per-launch Defaults, the resolved SidecarSpec, the external StatusSnapshot,
// and the spec/image resolution that turns a configured MCP server into a spec.

import (
	"fmt"
	"strings"
	"time"

	"petris.dev/toby/config/app"
	sandboxruntime "petris.dev/toby/sandbox/runtime"
)

type TransportType string

const (
	TransportStdio TransportType = appconfig.MCPTransportStdio
	TransportHTTP  TransportType = appconfig.MCPTransportHTTP
)

type Status string

const (
	StatusRegistered Status = "registered"
	StatusStarting   Status = "starting"
	StatusRunning    Status = "running"
	StatusExited     Status = "exited"
	StatusFailed     Status = "failed"
	StatusStopped    Status = "stopped"
)

type Defaults struct {
	// Image is the sandbox container image, used for any MCP server that does
	// not specify its own image or build.
	Image string
	Debug bool
}

type SidecarSpec struct {
	Name      string
	Transport TransportType
	Command   []string
	Env       map[string]string
	Image     string
	HTTPPort  int
	HTTPPath  string
	Debug     bool
}

type StatusSnapshot struct {
	Name        string
	Status      Status
	URL         string
	Transport   TransportType
	PID         int
	ExitCode    int
	LastError   string
	UpdatedAt   time.Time
	RuntimeInfo map[string]any
}

type ProcessResult struct {
	ExitCode int
	Err      error
}

func sidecarSpec(name string, server appconfig.MCPServer, defaults Defaults) (SidecarSpec, error) {
	command, err := server.CommandParts()
	if err != nil {
		return SidecarSpec{}, fmt.Errorf("mcp.%s: %w", name, err)
	}
	env, err := server.Environment()
	if err != nil {
		return SidecarSpec{}, fmt.Errorf("mcp.%s: %w", name, err)
	}
	transport := server.Transport()
	spec := SidecarSpec{
		Name:      name,
		Transport: TransportType(transport),
		Command:   command,
		Env:       env,
		Image:     sidecarImage(server, defaults),
		HTTPPort:  server.Port(),
		HTTPPath:  server.Path(),
		Debug:     defaults.Debug,
	}
	if spec.Transport == TransportHTTP && spec.HTTPPort <= 0 {
		return SidecarSpec{}, fmt.Errorf("mcp.%s.port is required for http transport", name)
	}
	return spec, nil
}

// sidecarImage picks the sidecar image: the server's own override, then the
// sandbox container image, then the built-in default.
func sidecarImage(server appconfig.MCPServer, defaults Defaults) string {
	if image := server.Image(); image != "" {
		return image
	}
	if image := strings.TrimSpace(defaults.Image); image != "" {
		return image
	}
	return sandboxruntime.DefaultImage
}
