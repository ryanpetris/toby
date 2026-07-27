package bwrap

// Builds a complete canonical run Plan from launch inputs while fixing Toby's
// reserved targets, host-network policy, and command capability policy.

import (
	"path"

	"petris.dev/toby/internal/sandbox/layout"
	"petris.dev/toby/internal/sandbox/mount"
)

// ProjectInput identifies a host project; BuildPlan assigns its reserved
// workspace target from Name.
type ProjectInput struct {
	Name     string
	HostPath string
	ReadOnly bool
}

// PlanInput contains the variable launch state from which BuildPlan derives a
// complete sandbox plan.
type PlanInput struct {
	RunID              string
	RootFS             RootFS
	Overlay            Overlay
	Home               Home
	Projects           []ProjectInput
	ManagedDirectories []mount.Entry
	Binds              []mount.Bind
	RuntimeAssets      []RuntimeAsset
	GeneratedFiles     []GeneratedFile
	SandboxBinaryPath  string
	Workdir            string
	Environment        []EnvironmentVariable
	Identity           Identity
	CommandArgv        []string
	ExecutionMode      ExecutionMode
	RootCommand        bool
}

// BuildPlan derives all fixed policy fields, validates the complete graph, and
// returns a detached canonical plan.
func BuildPlan(input PlanInput) (Plan, error) {
	projects := make([]Project, len(input.Projects))
	for index, project := range input.Projects {
		projects[index] = Project{
			Name:     project.Name,
			HostPath: project.HostPath,
			Target:   path.Join(layout.Workspace, project.Name),
			ReadOnly: project.ReadOnly,
		}
	}

	capabilities := CapabilityDropAll
	if input.RootCommand {
		capabilities = CapabilityRootLifecycle
	}
	plan := Plan{
		RunID:              input.RunID,
		RootFS:             input.RootFS,
		Overlay:            input.Overlay,
		Home:               input.Home,
		Projects:           projects,
		ManagedDirectories: input.ManagedDirectories,
		Binds:              input.Binds,
		RuntimeAssets:      input.RuntimeAssets,
		GeneratedFiles:     input.GeneratedFiles,
		SandboxBinary: Binary{
			HostPath: input.SandboxBinaryPath,
			Target:   layout.SandboxBinary(),
		},
		Workdir:     input.Workdir,
		Environment: input.Environment,
		Identity:    input.Identity,
		Namespaces: Namespaces{
			Network: NetworkHost,
		},
		Command: Command{
			Argv:         input.CommandArgv,
			Mode:         input.ExecutionMode,
			Root:         input.RootCommand,
			Capabilities: capabilities,
		},
	}.Canonical()
	if err := plan.Validate(); err != nil {
		return Plan{}, err
	}

	return plan, nil
}
