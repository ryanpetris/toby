package kit

import (
	"reflect"
	"testing"

	"petris.dev/toby/tools"
)

func TestSimpleMapsConfiguration(t *testing.T) {
	base := tools.Base{Metadata: tools.Metadata{Name: "example", LaunchHelp: "Launch Example", Group: tools.GroupSystem, ContextGroups: []string{tools.GroupSystem}}}
	install := []string{"npm", "install", "-g", "example"}
	env := map[string]string{"EXAMPLE": "1"}

	simple := NewSimple(nil, base, install, env)

	if simple.Name() != "example" || simple.LaunchHelp() != "Launch Example" {
		t.Fatalf("simple metadata = %#v", simple)
	}
	if !reflect.DeepEqual(simple.InstallCommand, install) || !reflect.DeepEqual(simple.SandboxEnv, env) {
		t.Fatalf("simple config = %#v", simple)
	}
}
