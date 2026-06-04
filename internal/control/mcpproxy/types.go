package mcpproxy

import (
	"fmt"
	"strings"
	"time"

	"petris.dev/toby/internal/config/toby"
	"petris.dev/toby/internal/sandbox/docker"
)

type RuntimeType string

const (
	RuntimeDocker RuntimeType = tobyconfig.MCPRuntimeDocker
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
	Runtime        tobyconfig.MCPRuntimeConfig
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
	runtime := defaults.Runtime
	runtime.Merge(server.Runtime())
	if strings.TrimSpace(runtime.Type) == "" {
		runtime.Type = tobyconfig.MCPRuntimeDocker
	}
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
		Runtime:   RuntimeType(runtime.Type),
		Transport: TransportType(transport),
		Command:   command,
		Env:       env,
		Image:     dockerImage(runtime, defaults),
		HTTPPort:  server.Port(),
		HTTPPath:  server.Path(),
		Debug:     defaults.Debug,
	}
	if spec.Transport == TransportHTTP && spec.HTTPPort <= 0 {
		return SidecarSpec{}, fmt.Errorf("mcp.%s.port is required for http transport", name)
	}
	return spec, nil
}

func dockerImage(runtime tobyconfig.MCPRuntimeConfig, defaults Defaults) string {
	if strings.TrimSpace(runtime.Docker.Image) != "" {
		return strings.TrimSpace(runtime.Docker.Image)
	}
	if strings.TrimSpace(defaults.EffectiveImage) != "" {
		return strings.TrimSpace(defaults.EffectiveImage)
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
