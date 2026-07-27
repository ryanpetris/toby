// Package tools defines the contract for a Toby tool and the registry that
// collects them. A tool is a development program Toby launches and manages
// (OpenCode, Claude Code, npm, …); each concrete tool lives in a subpackage and
// registers itself into the fx "tools" group as a Tool. The Registry builds
// an ordered Toolset for a requested launch, and the lifecycle package drives
// the Toolset through its phases.
package tools

import (
	"context"
)

// Context-injection groups a tool may belong to.
const (
	// GroupAI contains AI coding tools.
	GroupAI = "ai"
	// GroupUI contains user-interface support tools.
	GroupUI = "ui"
	// GroupSystem contains sandbox system tools.
	GroupSystem = "system"
	// GroupVCS contains version-control tools.
	GroupVCS = "vcs"
	// GroupCommand contains general command execution tools.
	GroupCommand = "command"
)

// Tool is the contract every tool implements. Embed Base for identity and no-op
// lifecycle defaults, then override only the phases the tool needs. Generated
// files and runtime assets use separate internal contributor contracts. The
// sandbox and other dependencies are injected at construction, so the phase
// methods take only a context. A tool declares its launch order
// relative to others purely through Dependencies (resolved by the registry into
// a topological order); there is no priority number.
type Tool interface {
	// Identity.
	// Name returns the stable identifier.
	Name() string
	// DisplayName returns the human-readable name.
	DisplayName() string
	// CommandName returns the CLI command name.
	CommandName() string
	// LaunchHelp returns the CLI launch description.
	LaunchHelp() string
	// Group returns the tool's CLI group.
	Group() string
	// ContextGroups returns the context groups enabled by the tool.
	ContextGroups() []string
	// Dependencies returns the tool dependencies.
	Dependencies() []string

	// Lifecycle phases, in order. PrepareHost and ConfigureSandbox contribute
	// declarations before the Bubblewrap plan is frozen; InitSandbox, Install,
	// and Launch execute against the attached run.
	// PrepareHost prepares host-side state required by the tool.
	PrepareHost(ctx context.Context, opts *Options) error
	// ConfigureSandbox adds the tool's sandbox configuration.
	ConfigureSandbox(ctx context.Context) error
	// InitSandbox initializes the tool inside the sandbox.
	InitSandbox(ctx context.Context) error
	// Install installs the tool when needed.
	Install(ctx context.Context, force bool) error
	// Launch runs the tool's foreground application.
	Launch(ctx context.Context, args []string) error
}

// Options is the launch-only configuration for one launch, shared by every tool
// in the launch. Config-corresponding values (image, pull policy, debug, yolo,
// and suppressed warnings) are not here: they are folded into the effective
// appconfig.Service at the launch boundary and read from there. Quiet is a
// launch-only presentation choice and intentionally has no configuration key.
type Options struct {
	Env      string
	Project  string
	Projects []ProjectMount
	Workdir  string
	Install  bool
	Upgrade  bool
	Quiet    bool
}

// ProjectMount names a host project to mount into the sandbox.
type ProjectMount struct {
	Name   string
	Source string

	// RequireProjectRoot restricts the opened source to XDG_PROJECTS_DIR or
	// one of its descendants.
	RequireProjectRoot bool
}
