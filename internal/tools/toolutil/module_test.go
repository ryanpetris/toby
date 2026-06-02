package toolutil

import (
	"reflect"
	"testing"

	"petris.dev/toby/internal/config"
	"petris.dev/toby/internal/tools/tool"
)

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
