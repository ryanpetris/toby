package opencode

import (
	"path/filepath"
	"testing"

	contextfiles "petris.dev/toby/context/files"
	"petris.dev/toby/internal/tools/builtin/npm"
	"petris.dev/toby/internal/tools/fake"
)

func TestOpenCodeDeclaresNPMDependency(t *testing.T) {
	home := t.TempDir()
	sandbox := fake.NewSandbox(filepath.Join(home, "runtime", "toby", "context"))
	contextFiles := contextfiles.NewService()
	contextFiles.SetSandbox(sandbox)
	oc := Provide(Params{

		Sandbox:      sandbox,
		ContextFiles: contextFiles,
	}).Service

	if got := oc.Dependencies(); len(got) != 1 || got[0] != npm.Name {
		t.Fatalf("dependency metadata = deps %#v", got)
	}
}
