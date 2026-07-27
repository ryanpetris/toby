package agent

// Verifies the standalone agent command surface without starting its Fx graph.

import (
	"io"
	"testing"

	"go.uber.org/fx"
)

func TestRootCommandExposesOnlyAgentProcessFlags(t *testing.T) {
	command := newRootCommand(nil, io.Discard, io.Discard)

	if len(command.Commands()) != 0 {
		t.Fatalf("tobyd subcommands = %d, want none", len(command.Commands()))
	}
	if command.Flags().Lookup("persistent") == nil {
		t.Fatal("tobyd persistent flag is missing")
	}
}

func TestRootCommandHelpDoesNotStartAgent(t *testing.T) {
	command := newRootCommand(
		[]string{"--help"},
		io.Discard,
		io.Discard,
	)

	if err := command.ExecuteContext(t.Context()); err != nil {
		t.Fatal(err)
	}
}

func TestModuleDependencyGraphIsValid(t *testing.T) {
	if err := fx.ValidateApp(module()); err != nil {
		t.Fatal(err)
	}
}
