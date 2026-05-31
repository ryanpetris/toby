package tool

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"petris.dev/toby/internal/diagnostic/warning"

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
	PathRoot     PathBase = "root"
	PathHome     PathBase = "home"
	PathRuntime  PathBase = "runtime"
	PathContext  PathBase = "context"
	PathBin      PathBase = "bin"
	PathProjects PathBase = "projects"

	DefaultSandboxRoot      = "/toby"
	DefaultSandboxHome      = "/toby/home"
	DefaultSandboxContext   = "/toby/context"
	DefaultSandboxBin       = "/toby/bin"
	DefaultSandboxWorkspace = "/toby/workspace"

	EnvTobyRoot      = "TOBY_ROOT"
	EnvTobyHome      = "TOBY_HOME"
	EnvTobyContext   = "TOBY_CONTEXT_DIR"
	EnvTobyBin       = "TOBY_BIN_DIR"
	EnvTobyWorkspace = "TOBY_WORKSPACE_DIR"
)

type SandboxPaths struct {
	Root      string
	Home      string
	Context   string
	Bin       string
	Workspace string
}

func DefaultSandboxPaths() SandboxPaths {
	return SandboxPaths{
		Root:      DefaultSandboxRoot,
		Home:      DefaultSandboxHome,
		Context:   DefaultSandboxContext,
		Bin:       DefaultSandboxBin,
		Workspace: DefaultSandboxWorkspace,
	}
}

type PathTarget struct {
	Base PathBase
	Path string
}

func AbsoluteTarget(path string) PathTarget { return PathTarget{Base: PathAbsolute, Path: path} }

func RootTarget(parts ...string) PathTarget { return pathTarget(PathRoot, parts...) }

func HomeTarget(parts ...string) PathTarget { return pathTarget(PathHome, parts...) }

func RuntimeTarget(parts ...string) PathTarget { return pathTarget(PathRuntime, parts...) }

func ContextTarget(parts ...string) PathTarget { return pathTarget(PathContext, parts...) }

func BinTarget(parts ...string) PathTarget { return pathTarget(PathBin, parts...) }

func ProjectsTarget(parts ...string) PathTarget { return pathTarget(PathProjects, parts...) }

func pathTarget(base PathBase, parts ...string) PathTarget {
	if len(parts) == 0 {
		return PathTarget{Base: base}
	}
	return PathTarget{Base: base, Path: filepath.ToSlash(filepath.Join(parts...))}
}

func ResolvePath(target PathTarget, sandbox Sandbox) string {
	switch target.Base {
	case PathRoot:
		return joinSandboxPath(sandbox.Paths().Root, target.Path)
	case PathHome:
		return joinSandboxPath(sandbox.Paths().Home, target.Path)
	case PathRuntime:
		return joinSandboxPath(sandbox.Paths().Root, target.Path)
	case PathContext:
		return joinSandboxPath(sandbox.Paths().Context, target.Path)
	case PathBin:
		return joinSandboxPath(sandbox.Paths().Bin, target.Path)
	case PathProjects:
		return joinSandboxPath(sandbox.Paths().Workspace, target.Path)
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
	DockerBuild      DockerBuildConfig
	BubblewrapRoot   string
	ToolStates       ToolStateSettings
	SuppressWarnings warning.Suppression
	Install          bool
	Upgrade          bool
	lifecycle        map[string]bool
}

type DockerBuildConfig struct {
	Context    string
	Dockerfile string
}

func (c DockerBuildConfig) IsSet() bool {
	return c.Context != ""
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
	Foreground bool
}

type SandboxService interface {
	Paths() SandboxPaths
	ProjectPath(string) (string, bool)
	VisibleHostPath(string) (string, error)
	GetEnvironment(string) (string, bool)
	SetEnvironment(context.Context, string, string) error
	PrependEnvironment(context.Context, string, string, string) error
	AppendEnvironment(context.Context, string, string, string) error
	AddFile(context.Context, string, []byte, uint32) error
	DeletePath(context.Context, string, bool) error
	Mkdir(context.Context, string, uint32) error
	Symlink(context.Context, string, string) error
	Exec(context.Context, []string, ExecOptions) (int, error)
	TobyMCPURL() string
}

type Sandbox interface {
	Paths() SandboxPaths
	HomeDir() string
	Projects() string
	TobyRuntimeDir() string
	TobyContextDir() string
	TobyOpenCodeConfigDir() string
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
	SandboxContextSetup(context.Context) error
	SandboxInit(context.Context) error
	Install(context.Context) error
	Upgrade(context.Context) error
	Launch(context.Context, []string) error
}

type ContextOptions struct {
	SuppressWarnings warning.Suppression
	Stderr           io.Writer
}

type ContextFileTool interface {
	RegisterContextFiles(context.Context, ContextOptions) error
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

func (b Base) SandboxContextSetup(context.Context) error { return nil }

func (b Base) SandboxInit(context.Context) error { return nil }

func (b Base) Install(context.Context) error { return nil }

func (b Base) Upgrade(context.Context) error { return nil }

func (b Base) Launch(context.Context, []string) error {
	return ErrNotLaunchable(b.Name())
}

func HomePath(home string, parts ...string) string {
	items := append([]string{home}, parts...)
	return filepath.Join(items...)
}
