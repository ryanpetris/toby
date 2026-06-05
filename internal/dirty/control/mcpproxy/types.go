package mcpproxy

import (
	"fmt"
	"strings"
	"time"

	"petris.dev/toby/config/toby"
	"petris.dev/toby/internal/dirty/sandbox/docker"
)

type RuntimeType string

const (
	RuntimeDocker RuntimeType = "docker"
)

type TransportType string

const (
	TransportStdio TransportType = tobyconfig.MCPTransportStdio
	TransportHTTP  TransportType = tobyconfig.MCPTransportHTTP
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
	// Image is the default MCP sidecar image (`mcp.image`).
	Image string
	// EffectiveImage is the container image, used as a final fallback.
	EffectiveImage string
	Debug          bool
}

type SidecarSpec struct {
	Name      string
	Runtime   RuntimeType
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
	Runtime     RuntimeType
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

func sidecarSpec(name string, server tobyconfig.MCPServer, defaults Defaults) (SidecarSpec, error) {
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
		Runtime:   RuntimeDocker,
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
// configured MCP default (`mcp.image`), then the container image, then the
// built-in default.
func sidecarImage(server tobyconfig.MCPServer, defaults Defaults) string {
	if image := server.Image(); image != "" {
		return image
	}
	if image := strings.TrimSpace(defaults.Image); image != "" {
		return image
	}
	if image := strings.TrimSpace(defaults.EffectiveImage); image != "" {
		return image
	}
	return docker.DefaultImage
}

func containerName(name string) string {
	replacer := strings.NewReplacer("/", "-", "_", "-", ".", "-")
	cleaned := replacer.Replace(strings.TrimSpace(name))
	if cleaned == "" {
		cleaned = "mcp"
	}
	return fmt.Sprintf("toby-mcp-%d-%d-%s", time.Now().UnixNano(), randomish(), cleaned)
}

func randomish() int64 {
	return time.Now().UnixNano() % 1000000
}
