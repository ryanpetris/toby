// Package tools defines the contract for a Toby tool and the registry that
// collects them. A tool is a development program Toby launches and manages
// (OpenCode, Claude Code, npm, …); each concrete tool lives in a subpackage and
// registers itself into the fx "tools" group as a Tool. The Registry builds
// an ordered Toolset for a requested launch, and the lifecycle package drives the
// Toolset through its phases. This mirrors the providers package.
package tools

import (
	"context"
	"io"

	"petris.dev/toby/diagnostic/warning"
)

// Group is the fx group name every tool registers into.
const Group = "tools"

// Context-injection groups a tool may belong to.
const (
	GroupAI      = "ai"
	GroupUI      = "ui"
	GroupSystem  = "system"
	GroupVCS     = "vcs"
	GroupCommand = "command"
)

// Canonical tool names. These are the stable identifiers used as config keys,
// dependency references, and CLI selectors.
const (
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

// Tool is the contract every tool implements. Embed Base for identity and no-op
// lifecycle defaults, then override only the phases the tool needs. Optional
// capabilities (writing context files) are separate interfaces a tool may also
// satisfy. The sandbox and other dependencies are injected at construction, so
// the phase methods take only a context.
type Tool interface {
	// Identity.
	Name() string
	CommandName() string
	LaunchHelp() string
	Group() string
	ContextGroups() []string
	Dependencies() []string
	LifecyclePriority() int

	// Lifecycle phases, in order. PrepareHost runs host-side before the sandbox
	// starts; the remaining phases run once the sandbox is up.
	PrepareHost(ctx context.Context, opts *Options) error
	ConfigureSandbox(ctx context.Context) error
	InitSandbox(ctx context.Context) error
	Install(ctx context.Context, force bool) error
	Launch(ctx context.Context, args []string) error
}

// ContextFileRegistrar is an optional capability: a tool that writes generated
// configuration/instruction files into the sandbox context directory.
type ContextFileRegistrar interface {
	RegisterContextFiles(ctx context.Context, opts ContextOptions) error
}

// ContextOptions carries cross-cutting inputs to context-file registration.
type ContextOptions struct {
	SuppressWarnings warning.Suppression
	Stderr           io.Writer
}

// Options is the fully resolved configuration for one launch, shared by every
// tool in the launch.
type Options struct {
	Env               string
	Project           string
	Projects          []ProjectMount
	Workdir           string
	SandboxRuntime    string
	Image             string
	Build             Build
	MountProfile      string
	ToolMountProfiles map[string]string
	SuppressWarnings  warning.Suppression
	Debug             *bool
	Yolo              *bool
	Install           bool
	Upgrade           bool
}

// Build describes a sandbox image to build from source instead of pulling.
type Build struct {
	Context    string
	Dockerfile string
}

// IsSet reports whether a build context was configured.
func (b Build) IsSet() bool { return b.Context != "" }

// DebugEnabled reports whether debug output is enabled for the launch.
func (o Options) DebugEnabled() bool { return o.Debug != nil && *o.Debug }

// YoloEnabled reports whether yolo mode is enabled for the launch.
func (o Options) YoloEnabled() bool { return o.Yolo != nil && *o.Yolo }

// ProjectMount names a host project to mount into the sandbox.
type ProjectMount struct {
	Name   string
	Source string
}
