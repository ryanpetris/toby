package tool

import (
	"context"
	"path/filepath"

	"github.com/spf13/cobra"
)

const (
	GroupAI     = "ai"
	GroupUI     = "ui"
	GroupSystem = "system"
	GroupVCS    = "vcs"

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

var GroupOrder = []string{GroupAI, GroupUI, GroupSystem, GroupVCS}

var ToolGroups = map[string][]string{
	GroupSystem: {DockerToolName, NpmToolName, UvToolName},
	GroupAI:     {OpenCodeToolName, CopilotToolName, ClaudeToolName, CodexToolName, GrokToolName, SpeckitToolName},
	GroupUI:     {T3ToolName, EmdashToolName},
	GroupVCS:    {GitHubCliToolName, GitLabCliToolName, ForgejoCliToolName},
}

type BindType string

const (
	BindRegular  BindType = "regular"
	BindReadOnly BindType = "read_only"
	BindDev      BindType = "dev"
)

type Bind struct {
	HostPath    string
	SandboxPath string
	Type        BindType
	Optional    bool
}

type CommandOptions struct {
	Env               string
	TmpEnv            bool
	Project           string
	MountableProjects bool
	Install           bool
	Upgrade           bool
	lifecycle         map[string]bool
}

type ExecOptions struct {
	HideOutput bool
}

type Executor func(context.Context, []string, ExecOptions) (int, error)

type Sandbox interface {
	HomeDir() string
}

type RunContext struct {
	Sandbox Sandbox
	Options *CommandOptions
	Extra   []string
	Toolset *Toolset
	Env     Environment
	Exec    Executor
	Launch  Executor
	// StaticMount is true when the read-only FUSE static files overlay (Toby MCP
	// binary and generated agent configuration) is mounted.
	StaticMount bool
	lifecycle   map[string]bool
}

type Tool interface {
	Name() string
	CommandName() string
	LaunchHelp() string
	ContextGroups() []string
	Binds() []Bind
	PathEntries() []string
	ConfigureCommand(*cobra.Command)
	HostInit(context.Context, *CommandOptions) error
	SandboxContextSetup(*RunContext) error
	SandboxInit(context.Context, *RunContext) error
	Install(context.Context, *RunContext) error
	Upgrade(context.Context, *RunContext) error
	Launch(context.Context, *RunContext) error
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

func (b Base) PathEntries() []string { return nil }

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
