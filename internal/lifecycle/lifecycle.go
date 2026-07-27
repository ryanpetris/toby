// Package lifecycle drives a tools.Toolset through the phases of a launch. The
// session calls Runner.RunPhase at each point in the launch sequence,
// interleaved with sandbox startup, and RunPhase invokes each active tool's
// phase method.
package lifecycle

import (
	"io"

	"petris.dev/toby/internal/diagnostic/warning"
	"petris.dev/toby/internal/tools"
)

// Phase is a step in the launch lifecycle, run in declared order.
type Phase int

const (
	// PhaseHostPrepare runs host-side before the sandbox starts (declare mounts).
	PhaseHostPrepare Phase = iota
	// PhaseConfigureSandbox seeds the sandbox environment.
	PhaseConfigureSandbox
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
	Checkpoint       func() error
}
