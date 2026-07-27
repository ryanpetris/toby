package tools

// Metadata and Base: a tool's declarative identity plus the no-op lifecycle
// defaults that concrete tools embed.

import (
	"context"
	"fmt"
)

// Metadata is a tool's declarative identity. Embed Base{Metadata: …} to satisfy
// the identity half of Tool.
type Metadata struct {
	Name          string
	DisplayName   string
	CLIName       string
	LaunchHelp    string
	Group         string
	ContextGroups []string
	Dependencies  []string
}

// CommandName is the name the tool is invoked as on the CLI, defaulting to Name.
func (m Metadata) CommandName() string {
	if m.CLIName != "" {
		return m.CLIName
	}
	return m.Name
}

// Base provides identity getters from Metadata and no-op lifecycle defaults so a
// tool only overrides the phases it cares about. Base itself is not launchable.
type Base struct {
	Metadata Metadata
}

// Name returns the stable identifier.
func (b Base) Name() string { return b.Metadata.Name }

// DisplayName returns the human-readable name.
func (b Base) DisplayName() string {
	if b.Metadata.DisplayName != "" {
		return b.Metadata.DisplayName
	}
	return b.Metadata.Name
}

// CommandName returns the CLI command name.
func (b Base) CommandName() string { return b.Metadata.CommandName() }

// LaunchHelp returns the CLI launch description.
func (b Base) LaunchHelp() string { return b.Metadata.LaunchHelp }

// Group returns the tool's CLI group.
func (b Base) Group() string { return b.Metadata.Group }

// ContextGroups returns the context groups enabled by the tool.
func (b Base) ContextGroups() []string { return append([]string(nil), b.Metadata.ContextGroups...) }

// Dependencies returns the tool dependencies.
func (b Base) Dependencies() []string { return append([]string(nil), b.Metadata.Dependencies...) }

// PrepareHost prepares host-side state required by the tool.
func (b Base) PrepareHost(context.Context, *Options) error { return nil }

// ConfigureSandbox adds the tool's sandbox configuration.
func (b Base) ConfigureSandbox(context.Context) error { return nil }

// InitSandbox initializes the tool inside the sandbox.
func (b Base) InitSandbox(context.Context) error { return nil }

// Install installs the tool when needed.
func (b Base) Install(context.Context, bool) error { return nil }

// Launch runs the tool's foreground application.
func (b Base) Launch(context.Context, []string) error {
	return ErrNotLaunchable(b.Name())
}

// ErrNotLaunchable reports that a tool has no launch behavior.
func ErrNotLaunchable(name string) error {
	return fmt.Errorf("tool %q is not launchable", name)
}
