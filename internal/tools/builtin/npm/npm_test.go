package npm

// Tests npm initialization and launch behavior.

import (
	"context"
	"path/filepath"
	"reflect"
	"testing"

	sandboxapi "petris.dev/toby/internal/sandbox"
	"petris.dev/toby/internal/sandbox/layout"
	"petris.dev/toby/internal/tools"
	"petris.dev/toby/internal/tools/fake"
)

func TestSandboxInitRunsNativeRuntimeAsset(t *testing.T) {
	var got []string
	var gotOptions sandboxapi.ExecOptions
	sandbox := fake.NewSandbox()
	sandbox.ExecFunc = func(
		_ context.Context,
		argv []string,
		options sandboxapi.ExecOptions,
	) (int, error) {
		got = append([]string(nil), argv...)
		gotOptions = options
		return 0, nil
	}
	svc := newTestNPM(t, sandbox)

	if err := svc.InitSandbox(context.Background()); err != nil {
		t.Fatal(err)
	}

	want := []string{filepath.Join(layout.Runtime, filepath.FromSlash(npmSandboxInitPath))}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("argv = %#v, want %#v", got, want)
	}
	if gotOptions.Status != "Preparing storage" {
		t.Fatalf("init status = %q", gotOptions.Status)
	}
}

func newTestNPM(t *testing.T, sandbox sandboxapi.Service) tools.Tool {
	t.Helper()
	return provide(params{Sandbox: sandbox}).Service
}
