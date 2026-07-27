// Package sandbox defines the minimal contract tools use to declare native
// mounts, configure their environment, and execute commands in a sandbox.
package sandbox

import (
	"context"

	"petris.dev/toby/internal/sandbox/mount"
)

// ExecOptions tunes how Service.Exec runs a command in the sandbox.
type ExecOptions struct {
	HideOutput bool
	Foreground bool
	Root       bool
	// Status is the safe child-operation label shown while a lifecycle command
	// runs. Callers should describe the command's intent rather than its
	// executable or implementation; an empty value uses a generic label.
	Status string
}

// Service spans declaration and execution for one native sandbox run. Generated
// files and runtime assets use their dedicated host-side contributor contracts.
type Service interface {
	// ProjectPath returns the configured project path.
	ProjectPath(string) (string, bool)
	// VisibleHostPath resolves a host path visible to the sandbox.
	VisibleHostPath(string) (string, error)
	// Environment returns a copy of the configured environment.
	Environment(string) (string, bool)
	// SetEnvironment sets an environment variable.
	SetEnvironment(context.Context, string, string) error
	// PrependEnvironment prepends a value to an environment variable.
	PrependEnvironment(context.Context, string, string, string) error
	// AppendEnvironment appends a value to an environment variable.
	AppendEnvironment(context.Context, string, string, string) error
	// AddBind adds a host bind to the sandbox plan.
	AddBind(mount.Bind) error
	// AddMount adds a managed volume to the sandbox plan.
	AddMount(mount.Request) error
	// Exec executes a command inside the attached sandbox.
	Exec(context.Context, []string, ExecOptions) (int, error)
}
