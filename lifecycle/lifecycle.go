// Package lifecycle drives a tools.Toolset through the phases of a launch. The
// session calls Runner.RunPhase at each point in the launch sequence (interleaved
// with sandbox startup); RunPhase invokes each active tool's phase method and any
// registered non-tool Hooks for that phase, ordered by priority. It replaces the
// older per-phase fx hook-group fan-out with one runner and one hook abstraction.
package lifecycle

import (
	"context"
	"io"

	"petris.dev/toby/diagnostic/warning"
	"petris.dev/toby/tools"
)

// Group is the fx group non-tool lifecycle Hooks register into.
const Group = "lifecycle"

// Phase is a step in the launch lifecycle, run in declared order.
type Phase int

const (
	// PhaseHostPrepare runs host-side before the sandbox starts (declare mounts).
	PhaseHostPrepare Phase = iota
	// PhaseConfigureSandbox seeds the sandbox environment.
	PhaseConfigureSandbox
	// PhaseContextFiles writes generated configuration/instruction files.
	PhaseContextFiles
	// PhaseInitSandbox runs in-sandbox initialization commands.
	PhaseInitSandbox
	// PhaseInstall installs (or, with force, upgrades) tools in the sandbox.
	PhaseInstall
)

// Context carries cross-cutting inputs available to every phase action.
type Context struct {
	Options          *tools.Options
	Stderr           io.Writer
	SuppressWarnings warning.Suppression
}

// Hook is a non-tool participant in a phase (e.g. writing Toby's own context
// files). Tools participate via their Tool phase methods, not Hooks.
type Hook struct {
	Phase    Phase
	Name     string
	Priority int
	Run      func(context.Context, Context) error
}
