package tool

import (
	"context"
	"io"

	"petris.dev/toby/internal/diagnostic/warning"
	sandboxmount "petris.dev/toby/internal/sandbox/mount"
	sandboxpath "petris.dev/toby/internal/sandbox/path"

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

type CommandOptions struct {
	Env               string
	Project           string
	Projects          []ProjectMount
	Workdir           string
	SandboxRuntime    string
	DockerImage       string
	DockerHome        string
	DockerProjects    string
	DockerBuild       DockerBuildConfig
	BubblewrapRoot    string
	MountProfile      string
	MountProfiles     sandboxmount.Profiles
	ToolMountProfiles map[string]string
	SuppressWarnings  warning.Suppression
	Debug             *bool
	Yolo              *bool
	Install           bool
	Upgrade           bool
	lifecycle         map[string]bool
}

type DockerBuildConfig struct {
	Context    string
	Dockerfile string
}

func (c DockerBuildConfig) IsSet() bool {
	return c.Context != ""
}

func (o CommandOptions) DebugEnabled() bool {
	return o.Debug != nil && *o.Debug
}

func (o CommandOptions) YoloEnabled() bool {
	return o.Yolo != nil && *o.Yolo
}

type ProjectMount struct {
	Name   string
	Source string
}

type ExecOptions struct {
	HideOutput bool
	Foreground bool
	Root       bool
}

type SandboxService interface {
	Paths() sandboxpath.Paths
	ProjectPath(string) (string, bool)
	VisibleHostPath(string) (string, error)
	GetEnvironment(string) (string, bool)
	SetEnvironment(context.Context, string, string) error
	PrependEnvironment(context.Context, string, string, string) error
	AppendEnvironment(context.Context, string, string, string) error
	AddBind(sandboxmount.Bind) error
	AddMount(sandboxmount.Request) (sandboxmount.Info, error)
	Mount(sandboxmount.Key) (sandboxmount.Info, bool)
	AddFile(context.Context, string, []byte, uint32) error
	AddFileOwned(context.Context, string, []byte, uint32, int, int) error
	DeletePath(context.Context, string, bool) error
	Mkdir(context.Context, string, uint32) error
	MkdirOwned(context.Context, string, uint32, int, int) error
	Symlink(context.Context, string, string) error
	SymlinkOwned(context.Context, string, string, int, int) error
	Exec(context.Context, []string, ExecOptions) (int, error)
	TobyMCPURL() string
}

type Sandbox interface {
	Paths() sandboxpath.Paths
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
	Dependencies() []string
	LifecyclePriority() int
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
	Dependencies  []string
	Priority      int
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

func (b Base) Dependencies() []string { return append([]string(nil), b.Metadata.Dependencies...) }

func (b Base) LifecyclePriority() int {
	if b.Metadata.Priority != 0 {
		return b.Metadata.Priority
	}
	return 10
}

func (b Base) ConfigureCommand(*cobra.Command) {}

func (b Base) HostInit(context.Context, *CommandOptions) error { return nil }

func (b Base) SandboxContextSetup(context.Context) error { return nil }

func (b Base) SandboxInit(context.Context) error { return nil }

func (b Base) Install(context.Context) error { return nil }

func (b Base) Upgrade(context.Context) error { return nil }

func (b Base) Launch(context.Context, []string) error {
	return ErrNotLaunchable(b.Name())
}
