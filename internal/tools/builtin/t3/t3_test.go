package t3

// Tests T3 installation and launch behavior.

import (
	"context"
	"path/filepath"
	"reflect"
	"testing"

	sandboxapi "petris.dev/toby/internal/sandbox"
	"petris.dev/toby/internal/sandbox/layout"
	"petris.dev/toby/internal/tools/fake"
)

func TestLaunchRunsNativeRuntimeWrapper(t *testing.T) {
	var got []string
	svc, sandbox := newTestT3(t)
	sandbox.ExecFunc = func(_ context.Context, argv []string, _ sandboxapi.ExecOptions) (int, error) {
		got = append([]string(nil), argv...)
		return 0, nil
	}

	if err := svc.Launch(context.Background(), []string{"--foo", "bar"}); err != nil {
		t.Fatal(err)
	}

	want := []string{filepath.Join(layout.Runtime, filepath.FromSlash(t3WrapperPath)), "--foo", "bar"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("argv = %#v, want %#v", got, want)
	}
}

func TestProvideChecksInstalledT3Binary(t *testing.T) {
	svc, _ := newTestT3(t)
	if svc.InstallCheckCommand != "t3" {
		t.Fatalf("InstallCheckCommand = %q, want t3", svc.InstallCheckCommand)
	}
}

func newTestT3(t *testing.T) (*t3Tool, *fake.Sandbox) {
	t.Helper()
	sandbox := fake.NewSandbox()
	return provide(params{Sandbox: sandbox}).Service.(*t3Tool), sandbox
}
