package bwrap

// Verifies that pure plan construction derives fixed sandbox policy and does
// not retain caller-owned slices.

import (
	"reflect"
	"testing"

	"petris.dev/toby/internal/sandbox/layout"
)

func TestBuildPlanDerivesReservedTargetsAndPolicies(t *testing.T) {
	base := validPlan()
	input := PlanInput{
		RunID:              base.RunID,
		RootFS:             base.RootFS,
		Overlay:            base.Overlay,
		Home:               base.Home,
		ManagedDirectories: base.ManagedDirectories,
		GeneratedFiles:     base.GeneratedFiles,
		SandboxBinaryPath:  base.SandboxBinary.HostPath,
		Workdir:            base.Workdir,
		Environment:        base.Environment,
		Identity:           base.Identity,
		CommandArgv:        []string{"sh", "-c", "true"},
		ExecutionMode:      ExecutionManagedPTY,
		RootCommand:        true,
		Projects: []ProjectInput{{
			Name:     "app",
			HostPath: "/projects/app",
			ReadOnly: true,
		}},
	}

	plan, err := BuildPlan(input)
	if err != nil {
		t.Fatal(err)
	}
	if plan.Projects[0].Target != layout.Workspace+"/app" ||
		!plan.Projects[0].ReadOnly {
		t.Fatalf("derived project = %#v", plan.Projects[0])
	}
	if plan.SandboxBinary.Target != layout.SandboxBinary() {
		t.Fatalf("sandbox helper target = %q", plan.SandboxBinary.Target)
	}
	if plan.Namespaces.Network != NetworkHost {
		t.Fatalf("network mode = %q", plan.Namespaces.Network)
	}
	if !plan.Command.Root ||
		plan.Command.Capabilities != CapabilityRootLifecycle {
		t.Fatalf("root command policy = %#v", plan.Command)
	}

	input.CommandArgv[0] = "mutated"
	input.Projects[0].Name = "mutated"
	if reflect.DeepEqual(plan.Command.Argv, input.CommandArgv) ||
		plan.Projects[0].Name != "app" {
		t.Fatal("built plan aliases its input")
	}
}

func TestBuildPlanDerivesApplicationCapabilityDrop(t *testing.T) {
	base := validPlan()
	input := PlanInput{
		RunID:              base.RunID,
		RootFS:             base.RootFS,
		Overlay:            base.Overlay,
		Home:               base.Home,
		Projects:           []ProjectInput{{Name: "app", HostPath: "/projects/app"}},
		ManagedDirectories: base.ManagedDirectories,
		GeneratedFiles:     base.GeneratedFiles,
		SandboxBinaryPath:  base.SandboxBinary.HostPath,
		Workdir:            base.Workdir,
		Environment:        base.Environment,
		Identity:           base.Identity,
		CommandArgv:        []string{"sh"},
		ExecutionMode:      ExecutionNonInteractive,
	}

	plan, err := BuildPlan(input)
	if err != nil {
		t.Fatal(err)
	}
	if plan.Command.Root ||
		plan.Command.Capabilities != CapabilityDropAll {
		t.Fatalf("application command policy = %#v", plan.Command)
	}
}
