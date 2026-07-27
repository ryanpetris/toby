package kit

// Verifies semantic lifecycle labels for reusable tool installations.

import (
	"context"
	"reflect"
	"testing"

	"petris.dev/toby/internal/sandbox"
	"petris.dev/toby/internal/tools"
	"petris.dev/toby/internal/tools/fake"
)

func TestSimpleInstallUsesIntentBasedStatuses(t *testing.T) {
	var commands [][]string
	var options []sandbox.ExecOptions
	sbx := fake.NewSandbox()
	sbx.ExecFunc = func(
		_ context.Context,
		argv []string,
		opts sandbox.ExecOptions,
	) (int, error) {
		commands = append(commands, append([]string(nil), argv...))
		options = append(options, opts)
		if len(argv) != 0 && argv[0] == "which" {
			return 1, nil
		}
		return 0, nil
	}
	simple := NewSimple(
		sbx,
		tools.Base{Metadata: tools.Metadata{Name: "example"}},
		nil,
		[]string{"package-manager", "install", "example"},
		nil,
	)

	if err := simple.Install(t.Context(), false); err != nil {
		t.Fatal(err)
	}
	wantCommands := [][]string{
		{"which", "example"},
		{"package-manager", "install", "example"},
	}
	if !reflect.DeepEqual(commands, wantCommands) {
		t.Fatalf("commands = %#v, want %#v", commands, wantCommands)
	}
	if len(options) != 2 {
		t.Fatalf("options = %#v", options)
	}
	if !options[0].HideOutput ||
		options[0].Status != "Checking installation" {
		t.Fatalf("check options = %#v", options[0])
	}
	if options[1].Status != "Installing" {
		t.Fatalf("install options = %#v", options[1])
	}
}
