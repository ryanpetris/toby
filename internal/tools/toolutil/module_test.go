package toolutil

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"petris.dev/toby/internal/config"
	"petris.dev/toby/internal/tools/tool"
)

type recordingTool struct {
	tool.Base
	binds   []tool.Bind
	entries []tool.PathTarget
	calls   *[]string
	err     error
}

func (t recordingTool) Binds() []tool.Bind { return append([]tool.Bind(nil), t.binds...) }

func (t recordingTool) PathEntries() []tool.PathTarget {
	return append([]tool.PathTarget(nil), t.entries...)
}

func (t recordingTool) Install(context.Context, *tool.RunContext) error {
	*t.calls = append(*t.calls, "install:"+t.Name())
	return t.err
}

func TestBindsDeduplicatesDependenciesBeforeOwnBinds(t *testing.T) {
	shared := tool.Bind{HostPath: "/host/shared", Target: tool.HomeTarget("shared")}
	depBind := tool.Bind{HostPath: "/host/dep", Target: tool.HomeTarget("dep")}
	ownBind := tool.Bind{HostPath: "/host/own", Target: tool.HomeTarget("own")}
	dep := recordingTool{binds: []tool.Bind{shared, depBind}}

	got := Binds([]tool.Tool{dep}, []tool.Bind{shared, ownBind})
	want := []tool.Bind{shared, depBind, ownBind}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Binds = %#v, want %#v", got, want)
	}
}

func TestPathEntriesDeduplicatesDependenciesBeforeOwnEntries(t *testing.T) {
	shared := tool.HomeTarget(".local", "bin")
	depEntry := tool.HomeTarget("dep", "bin")
	ownEntry := tool.HomeTarget("own", "bin")
	dep := recordingTool{entries: []tool.PathTarget{shared, depEntry}}

	got := PathEntries([]tool.Tool{dep}, []tool.PathTarget{shared, ownEntry})
	want := []tool.PathTarget{shared, depEntry, ownEntry}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("PathEntries = %#v, want %#v", got, want)
	}
}

func TestInstallDependenciesStopsOnFirstError(t *testing.T) {
	boom := errors.New("boom")
	var calls []string
	deps := []tool.Tool{
		recordingTool{Base: tool.Base{Metadata: tool.Metadata{Name: "a"}}, calls: &calls},
		recordingTool{Base: tool.Base{Metadata: tool.Metadata{Name: "b"}}, calls: &calls, err: boom},
		recordingTool{Base: tool.Base{Metadata: tool.Metadata{Name: "c"}}, calls: &calls},
	}

	err := InstallDependencies(context.Background(), &tool.RunContext{}, deps...)
	if !errors.Is(err, boom) {
		t.Fatalf("err = %v, want boom", err)
	}
	want := []string{"install:a", "install:b"}
	if !reflect.DeepEqual(calls, want) {
		t.Fatalf("calls = %#v, want %#v", calls, want)
	}
}

func TestSimpleMapsPathsAndConfiguration(t *testing.T) {
	paths := config.Paths{SandboxRoot: "/tmp/toby/sandboxes"}
	base := Base("example", "Launch Example", tool.GroupSystem)
	install := []string{"npm", "install", "-g", "example"}
	env := map[string]string{"EXAMPLE": "1"}

	simple := Simple(paths, base, []string{".example"}, []string{".config", "example"}, install, env)

	if simple.RootDir != paths.SandboxRoot || simple.Name() != "example" || simple.LaunchHelp() != "Launch Example" {
		t.Fatalf("simple metadata = %#v", simple)
	}
	if !reflect.DeepEqual(simple.HostSubpath, []string{".example"}) || !reflect.DeepEqual(simple.SandboxSubpath, []string{".config", "example"}) {
		t.Fatalf("simple paths = %#v", simple)
	}
	if !reflect.DeepEqual(simple.InstallCommand, install) || !reflect.DeepEqual(simple.SandboxEnv, env) {
		t.Fatalf("simple config = %#v", simple)
	}
}
