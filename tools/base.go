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
	CLIName       string
	LaunchHelp    string
	Group         string
	ContextGroups []string
	Dependencies  []string
	Priority      int
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

func (b Base) Name() string            { return b.Metadata.Name }
func (b Base) CommandName() string     { return b.Metadata.CommandName() }
func (b Base) LaunchHelp() string      { return b.Metadata.LaunchHelp }
func (b Base) Group() string           { return b.Metadata.Group }
func (b Base) ContextGroups() []string { return append([]string(nil), b.Metadata.ContextGroups...) }
func (b Base) Dependencies() []string  { return append([]string(nil), b.Metadata.Dependencies...) }

func (b Base) LifecyclePriority() int {
	if b.Metadata.Priority != 0 {
		return b.Metadata.Priority
	}
	return 10
}

func (b Base) PrepareHost(context.Context, *Options) error { return nil }
func (b Base) ConfigureSandbox(context.Context) error      { return nil }
func (b Base) InitSandbox(context.Context) error           { return nil }
func (b Base) Install(context.Context, bool) error         { return nil }

func (b Base) Launch(context.Context, []string) error {
	return ErrNotLaunchable(b.Name())
}

// ErrNotLaunchable reports that a tool has no launch behavior.
func ErrNotLaunchable(name string) error {
	return fmt.Errorf("tool %q is not launchable", name)
}
