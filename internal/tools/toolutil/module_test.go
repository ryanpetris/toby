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
	calls *[]string
	err   error
}

func (t recordingTool) Install(context.Context) error {
	*t.calls = append(*t.calls, "install:"+t.Name())
	return t.err
}

func TestInstallDependenciesStopsOnFirstError(t *testing.T) {
	boom := errors.New("boom")
	var calls []string
	deps := []tool.Tool{
		recordingTool{Base: tool.Base{Metadata: tool.Metadata{Name: "a"}}, calls: &calls},
		recordingTool{Base: tool.Base{Metadata: tool.Metadata{Name: "b"}}, calls: &calls, err: boom},
		recordingTool{Base: tool.Base{Metadata: tool.Metadata{Name: "c"}}, calls: &calls},
	}

	err := InstallDependencies(context.Background(), deps...)
	if !errors.Is(err, boom) {
		t.Fatalf("err = %v, want boom", err)
	}
	want := []string{"install:a", "install:b"}
	if !reflect.DeepEqual(calls, want) {
		t.Fatalf("calls = %#v, want %#v", calls, want)
	}
}

func TestSimpleMapsPathsAndConfiguration(t *testing.T) {
	paths := config.Paths{SandboxRoot: "/cache/toby/sandboxes"}
	base := Base("example", "Launch Example", tool.GroupSystem)
	install := []string{"npm", "install", "-g", "example"}
	env := map[string]string{"EXAMPLE": "1"}

	simple := Simple(paths, nil, base, []string{".example"}, []string{".config", "example"}, install, env)

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
