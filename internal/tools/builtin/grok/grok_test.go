package grok

// Tests Grok's lifecycle and runtime-specific launch arguments.

import (
	"context"
	"path/filepath"
	"reflect"
	"testing"

	"petris.dev/toby/internal/config/session"
	sandboxapi "petris.dev/toby/internal/sandbox"
	"petris.dev/toby/internal/sandbox/layout"
	"petris.dev/toby/internal/sandbox/mount"
	"petris.dev/toby/internal/tools"
	"petris.dev/toby/internal/tools/fake"
)

func TestGrokHostInitRegistersManagedMount(t *testing.T) {
	sandbox := fake.NewSandbox()
	gr := provide(params{Sandbox: sandbox}).Service
	if err := gr.PrepareHost(context.Background(), &tools.Options{}); err != nil {
		t.Fatal(err)
	}
	if len(sandbox.Binds) != 0 {
		t.Fatalf("binds = %#v", sandbox.Binds)
	}
	if len(sandbox.Mounts) != 1 || sandbox.Mounts[0].Key != (mount.Key{Type: mount.TypeTool, Name: Name, Purpose: "state"}) || sandbox.Mounts[0].Target != filepath.Join(layout.Home, ".grok") {
		t.Fatalf("mounts = %#v", sandbox.Mounts)
	}
}

func TestLaunchAddsRules(t *testing.T) {
	gr, sandbox, holder := newTestGrok(t)
	holder.Set(sessionconfig.Config{
		Instructions: sessionconfig.Instructions{Contents: [][]byte{[]byte("# user instructions\n")}},
	})
	var got []string
	sandbox.ExecFunc = func(_ context.Context, argv []string, _ sandboxapi.ExecOptions) (int, error) {
		got = append([]string(nil), argv...)
		return 0, nil
	}

	if err := gr.Launch(context.Background(), []string{"--model", "grok-code-fast-1"}); err != nil {
		t.Fatal(err)
	}
	want := []string{"grok", "--rules", "# user instructions\n", "--model", "grok-code-fast-1"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("argv = %#v, want %#v", got, want)
	}
}

func newTestGrok(t *testing.T) (tools.Tool, *fake.Sandbox, *sessionconfig.Holder) {
	t.Helper()
	sandbox := fake.NewSandbox()
	holder := sessionconfig.NewHolder()
	tool := provide(params{Sandbox: sandbox, SessionConfig: holder}).Service
	return tool, sandbox, holder
}
