package opencode

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"petris.dev/toby/internal/config"
	"petris.dev/toby/internal/tool"
	"petris.dev/toby/internal/tools/npm"

	"go.uber.org/fx"
	"go.uber.org/fx/fxtest"
)

type fakeNPM struct{ tool.Base }

func (fakeNPM) PathEntries() []string { return []string{"/npm/bin"} }

func (fakeNPM) SandboxContextSetup(ctx *tool.RunContext) error {
	ctx.Env["NPM_CALLED"] = "1"
	ctx.Env["OPENCODE_CONFIG_DIR"] = "dependency"
	return nil
}

func TestOpenCodeSetsSyntheticConfigDir(t *testing.T) {
	home := t.TempDir()
	paths := config.Paths{Home: home, StateHome: filepath.Join(home, ".state"), SandboxRoot: filepath.Join(home, "sandboxes")}
	var oc tool.Tool
	app := fxtest.New(t,
		fx.Supply(paths),
		npm.Module,
		Module,
		fx.Invoke(func(params struct {
			fx.In

			OpenCode tool.Tool `name:"opencode"`
		}) {
			oc = params.OpenCode
		}),
	)
	app.RequireStart()
	t.Cleanup(app.RequireStop)
	run := &tool.RunContext{Options: &tool.CommandOptions{}, Env: tool.Environment{}}
	if err := oc.SandboxContextSetup(run); err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(home, ".state", "toby", "static", "opencode")
	if run.Env["OPENCODE_CONFIG_DIR"] != want {
		t.Fatalf("OPENCODE_CONFIG_DIR = %q, want %q", run.Env["OPENCODE_CONFIG_DIR"], want)
	}
}

func TestOpenCodeCallsDependencyBeforeOwnContextSetup(t *testing.T) {
	home := t.TempDir()
	paths := config.Paths{Home: home, StateHome: filepath.Join(home, ".state"), SandboxRoot: filepath.Join(home, "sandboxes")}
	oc := Provide(Params{
		Paths: paths,
		NPM:   fakeNPM{Base: tool.Base{Metadata: tool.Metadata{Name: tool.NpmToolName}}},
	}).Service

	if got, want := oc.PathEntries(), []string{"/npm/bin"}; len(got) != len(want) || got[0] != want[0] {
		t.Fatalf("PathEntries = %#v, want %#v", got, want)
	}
	run := &tool.RunContext{Options: &tool.CommandOptions{}, Env: tool.Environment{}}
	if err := oc.SandboxContextSetup(run); err != nil {
		t.Fatal(err)
	}
	if run.Env["NPM_CALLED"] != "1" {
		t.Fatalf("dependency SandboxContextSetup was not called")
	}
	want := filepath.Join(home, ".state", "toby", "static", "opencode")
	if run.Env["OPENCODE_CONFIG_DIR"] != want {
		t.Fatalf("OPENCODE_CONFIG_DIR = %q, want %q", run.Env["OPENCODE_CONFIG_DIR"], want)
	}
}

func TestOpenCodeCallsDependencyHostInitBeforeOwnHostInit(t *testing.T) {
	home := t.TempDir()
	paths := config.Paths{Home: home, StateHome: filepath.Join(home, ".state"), SandboxRoot: filepath.Join(home, "sandboxes")}
	called := false
	npm := hostInitNPM{Base: tool.Base{Metadata: tool.Metadata{Name: tool.NpmToolName}}, called: &called, sandboxRoot: paths.SandboxRoot}
	oc := Provide(Params{Paths: paths, NPM: npm}).Service
	if err := oc.HostInit(context.Background(), &tool.CommandOptions{}); err != nil {
		t.Fatal(err)
	}
	if !called {
		t.Fatalf("dependency HostInit was not called")
	}
	for _, dir := range []string{
		filepath.Join(paths.SandboxRoot, ".config", "opencode"),
		filepath.Join(paths.SandboxRoot, ".config", "opencode-share"),
	} {
		info, err := os.Stat(dir)
		if err != nil {
			t.Fatal(err)
		}
		if !info.IsDir() {
			t.Fatalf("%s is not a directory", dir)
		}
	}
}

type hostInitNPM struct {
	tool.Base
	called      *bool
	sandboxRoot string
}

func (t hostInitNPM) HostInit(context.Context, *tool.CommandOptions) error {
	if _, err := os.Stat(filepath.Join(t.sandboxRoot, ".config", "opencode")); err == nil {
		return fmt.Errorf("opencode HostInit ran before dependency HostInit")
	} else if !os.IsNotExist(err) {
		return err
	}
	*t.called = true
	return nil
}
