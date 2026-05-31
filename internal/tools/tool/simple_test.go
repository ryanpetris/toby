package tool

import (
	"context"
	"errors"
	"path/filepath"
	"reflect"
	"testing"
)

func TestSimpleBindsDefaultsAndSandboxSubpathFallback(t *testing.T) {
	simple := &Simple{RootDir: "/root", HostSubpath: []string{"state", "tool"}}
	binds := simple.Binds()
	want := []Bind{{
		HostPath: filepath.Join("/root", "state", "tool"),
		Target:   HomeTarget("state", "tool"),
		Type:     BindRegular,
		State:    true,
	}}
	if !reflect.DeepEqual(binds, want) {
		t.Fatalf("binds = %#v, want %#v", binds, want)
	}

	simple = &Simple{RootDir: "/root", HostSubpath: []string{"host"}, SandboxSubpath: []string{"sandbox"}, BindType: BindReadOnly}
	binds = simple.Binds()
	if len(binds) != 1 || binds[0].Target != HomeTarget("sandbox") || binds[0].Type != BindReadOnly {
		t.Fatalf("custom binds = %#v", binds)
	}

	if binds := (&Simple{}).Binds(); binds != nil {
		t.Fatalf("empty binds = %#v", binds)
	}
}

func TestSimpleLaunchUsesDefaultAndOverrideCommands(t *testing.T) {
	var calls [][]string
	exec := func(_ context.Context, argv []string, _ ExecOptions) (int, error) {
		calls = append(calls, append([]string(nil), argv...))
		return 0, nil
	}
	run := &RunContext{Extra: []string{"--help"}, Launch: exec}
	if err := (&Simple{Base: Base{Metadata: Metadata{Name: "tool"}}}).Launch(context.Background(), run); err != nil {
		t.Fatal(err)
	}
	if err := (&Simple{Base: Base{Metadata: Metadata{Name: "tool"}}, LaunchCommand: "custom"}).Launch(context.Background(), run); err != nil {
		t.Fatal(err)
	}
	want := [][]string{{"tool", "--help"}, {"custom", "--help"}}
	if !reflect.DeepEqual(calls, want) {
		t.Fatalf("calls = %#v, want %#v", calls, want)
	}
}

func TestRunCommandReturnsExecutorErrorsAndExitCodes(t *testing.T) {
	if err := RunCommand(context.Background(), func(context.Context, []string, ExecOptions) (int, error) { return 0, nil }, []string{"true"}, ExecOptions{}); err != nil {
		t.Fatal(err)
	}
	sentinel := errors.New("boom")
	if err := RunCommand(context.Background(), func(context.Context, []string, ExecOptions) (int, error) { return 0, sentinel }, []string{"bad"}, ExecOptions{}); !errors.Is(err, sentinel) {
		t.Fatalf("err = %v, want sentinel", err)
	}
	err := RunCommand(context.Background(), func(context.Context, []string, ExecOptions) (int, error) { return 7, nil }, []string{"false"}, ExecOptions{})
	var coded interface{ ExitCode() int }
	if !errors.As(err, &coded) || coded.ExitCode() != 7 {
		t.Fatalf("err = %#v, coded = %#v", err, coded)
	}
}
