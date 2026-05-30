package tool

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"petris.dev/toby/internal/contextfiles"
	"petris.dev/toby/internal/warning"

	"github.com/spf13/cobra"
)

const (
	GroupAI      = "ai"
	GroupUI      = "ui"
	GroupSystem  = "system"
	GroupVCS     = "vcs"
	GroupCommand = "command"

	ExecToolName       = "exec"
	NpmToolName        = "npm"
	UvToolName         = "uv"
	OpenCodeToolName   = "opencode"
	CopilotToolName    = "copilot"
	ClaudeToolName     = "claude"
	DockerToolName     = "docker"
	CodexToolName      = "codex"
	GrokToolName       = "grok"
	EmdashToolName     = "emdash"
	T3ToolName         = "t3"
	SpeckitToolName    = "speckit"
	GitHubCliToolName  = "github_cli"
	GitLabCliToolName  = "gitlab_cli"
	ForgejoCliToolName = "fj"
)

var GroupOrder = []string{GroupAI, GroupUI, GroupSystem, GroupVCS, GroupCommand}

var ToolGroups = map[string][]string{
	GroupCommand: {ExecToolName},
	GroupSystem:  {DockerToolName, NpmToolName, UvToolName},
	GroupAI:      {OpenCodeToolName, CopilotToolName, ClaudeToolName, CodexToolName, GrokToolName, SpeckitToolName},
	GroupUI:      {T3ToolName, EmdashToolName},
	GroupVCS:     {GitHubCliToolName, GitLabCliToolName, ForgejoCliToolName},
}

type BindType string

const (
	BindRegular  BindType = "regular"
	BindReadOnly BindType = "read_only"
	BindDev      BindType = "dev"
)

type Bind struct {
	HostPath  string
	Target    PathTarget
	Type      BindType
	Optional  bool
	State     bool
	StatePath string
}

type ToolState string

const (
	ToolStatePrivate ToolState = "private"
	ToolStateHost    ToolState = "host"
)

type ToolStateSettings struct {
	Default ToolStateConfig
	Tools   map[string]ToolStateConfig
}

type ToolStateConfig struct {
	State     ToolState
	StateRoot string
}

func ParseToolState(value string) (ToolState, error) {
	switch state := ToolState(strings.TrimSpace(value)); state {
	case ToolStatePrivate, ToolStateHost:
		return state, nil
	default:
		return "", fmt.Errorf("tool state must be %q or %q", ToolStatePrivate, ToolStateHost)
	}
}

func (s ToolStateSettings) Clone() ToolStateSettings {
	clone := ToolStateSettings{Default: s.Default}
	if len(s.Tools) > 0 {
		clone.Tools = make(map[string]ToolStateConfig, len(s.Tools))
		for name, cfg := range s.Tools {
			clone.Tools[name] = cfg
		}
	}
	return clone
}

func (s *ToolStateSettings) Merge(src ToolStateSettings) {
	if src.Default.State != "" {
		s.Default.State = src.Default.State
	}
	if src.Default.StateRoot != "" {
		s.Default.StateRoot = src.Default.StateRoot
	}
	if len(src.Tools) == 0 {
		return
	}
	if s.Tools == nil {
		s.Tools = map[string]ToolStateConfig{}
	}
	for name, srcCfg := range src.Tools {
		cfg := s.Tools[name]
		if srcCfg.State != "" {
			cfg.State = srcCfg.State
		}
		if srcCfg.StateRoot != "" {
			cfg.StateRoot = srcCfg.StateRoot
		}
		s.Tools[name] = cfg
	}
}

func (s ToolStateSettings) StateFor(name string) ToolState {
	cfg := s.configFor(name)
	if cfg.State != "" {
		return cfg.State
	}
	if name == DockerToolName {
		return ToolStateHost
	}
	return ToolStatePrivate
}

func (s ToolStateSettings) StateRootFor(name string) string {
	cfg := s.configFor(name)
	if cfg.StateRoot != "" {
		return cfg.StateRoot
	}
	if home, err := os.UserHomeDir(); err == nil {
		return home
	}
	return ""
}

func (s ToolStateSettings) configFor(name string) ToolStateConfig {
	name = strings.TrimSpace(name)
	cfg := s.Default
	if toolCfg, ok := s.Tools[name]; ok {
		if toolCfg.State != "" {
			cfg.State = toolCfg.State
		}
		if toolCfg.StateRoot != "" {
			cfg.StateRoot = toolCfg.StateRoot
		}
	}
	return cfg
}

func (s ToolStateSettings) ResolveStateRoots(home, base string) (ToolStateSettings, error) {
	resolved := s.Clone()
	if resolved.Default.StateRoot == "" {
		resolved.Default.StateRoot = home
	} else {
		root, err := ResolveStateRoot(resolved.Default.StateRoot, home, base)
		if err != nil {
			return ToolStateSettings{}, err
		}
		resolved.Default.StateRoot = root
	}
	for name, cfg := range resolved.Tools {
		if cfg.StateRoot == "" {
			continue
		}
		root, err := ResolveStateRoot(cfg.StateRoot, home, base)
		if err != nil {
			return ToolStateSettings{}, err
		}
		cfg.StateRoot = root
		resolved.Tools[name] = cfg
	}
	return resolved, nil
}

func ResolveStateRoot(value, home, base string) (string, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return "", fmt.Errorf("stateRoot must not be empty")
	}
	value = expandHome(value, home)
	if filepath.IsAbs(value) {
		return value, nil
	}
	if base == "" {
		base = "."
	}
	return filepath.Join(base, value), nil
}

func expandHome(path, home string) string {
	if path == "~" {
		return home
	}
	if strings.HasPrefix(path, "~/") {
		return home + path[1:]
	}
	return path
}

type PathBase string

const (
	PathAbsolute PathBase = "absolute"
	PathHome     PathBase = "home"
	PathRuntime  PathBase = "runtime"
	PathProjects PathBase = "projects"
)

type PathTarget struct {
	Base PathBase
	Path string
}

func AbsoluteTarget(path string) PathTarget { return PathTarget{Base: PathAbsolute, Path: path} }

func HomeTarget(parts ...string) PathTarget { return pathTarget(PathHome, parts...) }

func RuntimeTarget(parts ...string) PathTarget { return pathTarget(PathRuntime, parts...) }

func ProjectsTarget(parts ...string) PathTarget { return pathTarget(PathProjects, parts...) }

func pathTarget(base PathBase, parts ...string) PathTarget {
	if len(parts) == 0 {
		return PathTarget{Base: base}
	}
	return PathTarget{Base: base, Path: filepath.ToSlash(filepath.Join(parts...))}
}

func ResolvePath(target PathTarget, sandbox Sandbox) string {
	switch target.Base {
	case PathHome:
		return joinSandboxPath(sandbox.HomeDir(), target.Path)
	case PathRuntime:
		return joinSandboxPath(sandbox.TobyRuntimeDir(), target.Path)
	case PathProjects:
		return joinSandboxPath(sandbox.Projects(), target.Path)
	case PathAbsolute, "":
		return target.Path
	default:
		return target.Path
	}
}

func joinSandboxPath(base, rel string) string {
	if rel == "" {
		return base
	}
	return filepath.Join(base, filepath.FromSlash(rel))
}

type CommandOptions struct {
	Env              string
	Project          string
	Projects         []ProjectMount
	Workdir          string
	SandboxRuntime   string
	DockerImage      string
	DockerHome       string
	DockerProjects   string
	BubblewrapRoot   string
	ToolStates       ToolStateSettings
	SuppressWarnings warning.Suppression
	Install          bool
	Upgrade          bool
	lifecycle        map[string]bool
}

func (o *CommandOptions) ToolStateFor(name string) ToolState {
	if o == nil {
		return ToolStateSettings{}.StateFor(name)
	}
	return o.ToolStates.StateFor(name)
}

func (o *CommandOptions) ToolStateRootFor(name string) string {
	if o == nil {
		return ToolStateSettings{}.StateRootFor(name)
	}
	return o.ToolStates.StateRootFor(name)
}

type ProjectMount struct {
	Name   string
	Source string
}

type ExecOptions struct {
	HideOutput bool
}

type Executor func(context.Context, []string, ExecOptions) (int, error)

type Sandbox interface {
	HomeDir() string
	Projects() string
	TobyRuntimeDir() string
	TobyContextDir() string
	TobyOpenCodeConfigDir() string
}

type RunContext struct {
	Sandbox      Sandbox
	Options      *CommandOptions
	Extra        []string
	Toolset      *Toolset
	Env          Environment
	Stderr       io.Writer
	ContextFiles *contextfiles.Session
	Exec         Executor
	Launch       Executor
	lifecycle    map[string]bool
}

type Tool interface {
	Name() string
	CommandName() string
	LaunchHelp() string
	ContextGroups() []string
	Binds() []Bind
	PathEntries() []PathTarget
	ConfigureCommand(*cobra.Command)
	HostInit(context.Context, *CommandOptions) error
	SandboxContextSetup(*RunContext) error
	SandboxInit(context.Context, *RunContext) error
	Install(context.Context, *RunContext) error
	Upgrade(context.Context, *RunContext) error
	Launch(context.Context, *RunContext) error
}

type ContextFileTool interface {
	RegisterContextFiles(context.Context, *RunContext) error
}

type Metadata struct {
	Name          string
	CLIName       string
	LaunchHelp    string
	ContextGroups []string
}

func (m Metadata) CommandName() string {
	if m.CLIName != "" {
		return m.CLIName
	}
	return m.Name
}

type Base struct {
	Metadata Metadata
}

func (b Base) Name() string { return b.Metadata.Name }

func (b Base) CommandName() string { return b.Metadata.CommandName() }

func (b Base) LaunchHelp() string { return b.Metadata.LaunchHelp }

func (b Base) ContextGroups() []string { return append([]string(nil), b.Metadata.ContextGroups...) }

func (b Base) Binds() []Bind { return nil }

func (b Base) PathEntries() []PathTarget { return nil }

func (b Base) ConfigureCommand(*cobra.Command) {}

func (b Base) HostInit(context.Context, *CommandOptions) error { return nil }

func (b Base) SandboxContextSetup(*RunContext) error { return nil }

func (b Base) SandboxInit(context.Context, *RunContext) error { return nil }

func (b Base) Install(context.Context, *RunContext) error { return nil }

func (b Base) Upgrade(context.Context, *RunContext) error { return nil }

func (b Base) Launch(context.Context, *RunContext) error {
	return ErrNotLaunchable(b.Name())
}

func HomePath(home string, parts ...string) string {
	items := append([]string{home}, parts...)
	return filepath.Join(items...)
}
