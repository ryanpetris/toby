package emdash

// Tests emdash installation, upgrades, and launch behavior.

import (
	"context"
	"path/filepath"
	"reflect"
	"testing"

	sandboxapi "petris.dev/toby/internal/sandbox"
	"petris.dev/toby/internal/sandbox/layout"
	"petris.dev/toby/internal/tools/fake"
	"petris.dev/toby/internal/tools/runtimepath"
)

func TestConfigureSandboxAddsLocalBinToPath(t *testing.T) {
	sandbox := fake.NewSandbox()
	svc := provide(params{Sandbox: sandbox}).Service

	if err := svc.ConfigureSandbox(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got, want := sandbox.Env["PATH"], filepath.Join(layout.Home, ".local", "bin"); got != want {
		t.Fatalf("PATH = %q, want %q", got, want)
	}
}

func TestInstallPathUsesNativeRuntime(t *testing.T) {
	assetPath, err := runtimepath.Resolve(emdashInstallPath)
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join(layout.Runtime, filepath.FromSlash(emdashInstallPath)); assetPath != want {
		t.Fatalf("path = %q, want %q", assetPath, want)
	}
}

func TestInstallSkipsWhenBinaryExists(t *testing.T) {
	var calls [][]string
	sandbox := fake.NewSandbox()
	sandbox.ExecFunc = func(_ context.Context, argv []string, _ sandboxapi.ExecOptions) (int, error) {
		calls = append(calls, append([]string(nil), argv...))
		return 0, nil
	}
	svc := provide(params{Sandbox: sandbox}).Service

	if err := svc.Install(context.Background(), false); err != nil {
		t.Fatal(err)
	}
	want := [][]string{{"which", "emdash"}}
	if !reflect.DeepEqual(calls, want) {
		t.Fatalf("calls = %#v, want %#v", calls, want)
	}
}

func TestUpgradeRunsInstallerWithoutInstallCheck(t *testing.T) {
	var calls [][]string
	sandbox := fake.NewSandbox()
	sandbox.ExecFunc = func(_ context.Context, argv []string, _ sandboxapi.ExecOptions) (int, error) {
		calls = append(calls, append([]string(nil), argv...))
		return 0, nil
	}
	svc := provide(params{Sandbox: sandbox}).Service

	if err := svc.Install(context.Background(), true); err != nil {
		t.Fatal(err)
	}
	want := [][]string{{filepath.Join(layout.Runtime, filepath.FromSlash(emdashInstallPath)), appImageURL}}
	if !reflect.DeepEqual(calls, want) {
		t.Fatalf("calls = %#v, want %#v", calls, want)
	}
}

func TestLaunchRunsEmdashWithExtras(t *testing.T) {
	var got []string
	sandbox := fake.NewSandbox()
	sandbox.ExecFunc = func(_ context.Context, argv []string, _ sandboxapi.ExecOptions) (int, error) {
		got = append([]string(nil), argv...)
		return 0, nil
	}
	svc := provide(params{Sandbox: sandbox}).Service

	if err := svc.Launch(context.Background(), []string{"--open", "project"}); err != nil {
		t.Fatal(err)
	}
	want := []string{"emdash", "--open", "project"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("argv = %#v, want %#v", got, want)
	}
}
