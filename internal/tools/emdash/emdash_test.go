package emdash

import (
	"bytes"
	"context"
	"path/filepath"
	"reflect"
	"testing"

	contextfiles "petris.dev/toby/internal/context/files"
	"petris.dev/toby/internal/tools/tool"
	"petris.dev/toby/internal/tools/tooltest"
)

func TestRegisterContextFilesWritesInstaller(t *testing.T) {
	sandbox := tooltest.NewSandbox(filepath.Join(t.TempDir(), "context"))
	contextFiles := contextfiles.NewService()
	contextFiles.SetSandbox(sandbox)
	svc := Provide(Params{Sandbox: sandbox, ContextFiles: contextFiles}).Service.(tool.ContextFileTool)

	if err := svc.RegisterContextFiles(context.Background(), tool.ContextOptions{}); err != nil {
		t.Fatal(err)
	}
	files := sandbox.Files
	if len(files) != 1 || files[0].Path != emdashInstallPath || files[0].Mode != 0o755 {
		t.Fatalf("files = %#v", files)
	}
	if !bytes.Contains(files[0].Data, []byte("#!/bin/sh")) || !bytes.Contains(files[0].Data, []byte("APPIMAGE_URL")) {
		t.Fatalf("installer contents = %s", files[0].Data)
	}
}

func TestInstallLaunchPathUsesSandboxContext(t *testing.T) {
	contextDir := filepath.Join(t.TempDir(), "context")
	sandbox := tooltest.NewSandbox(contextDir)
	svc := Provide(Params{Sandbox: sandbox, ContextFiles: contextfiles.NewService()}).Service.(*emdashTool)
	path := svc.contextPath(emdashInstallPath)
	if want := filepath.Join(contextDir, filepath.FromSlash(emdashInstallPath)); path != want {
		t.Fatalf("path = %q, want %q", path, want)
	}
}

func TestInstallSkipsWhenBinaryExists(t *testing.T) {
	var calls [][]string
	sandbox := tooltest.NewSandbox(filepath.Join(t.TempDir(), "context"))
	sandbox.ExecFunc = func(_ context.Context, argv []string, _ tool.ExecOptions) (int, error) {
		calls = append(calls, append([]string(nil), argv...))
		return 0, nil
	}
	svc := Provide(Params{Sandbox: sandbox, ContextFiles: contextfiles.NewService()}).Service

	if err := svc.Install(context.Background()); err != nil {
		t.Fatal(err)
	}
	want := [][]string{{"which", "emdash"}}
	if !reflect.DeepEqual(calls, want) {
		t.Fatalf("calls = %#v, want %#v", calls, want)
	}
}

func TestUpgradeRunsInstallerWithoutInstallCheck(t *testing.T) {
	contextDir := filepath.Join(t.TempDir(), "context")
	var calls [][]string
	sandbox := tooltest.NewSandbox(contextDir)
	sandbox.ExecFunc = func(_ context.Context, argv []string, _ tool.ExecOptions) (int, error) {
		calls = append(calls, append([]string(nil), argv...))
		return 0, nil
	}
	svc := Provide(Params{Sandbox: sandbox, ContextFiles: contextfiles.NewService()}).Service

	if err := svc.Upgrade(context.Background()); err != nil {
		t.Fatal(err)
	}
	want := [][]string{{filepath.Join(contextDir, filepath.FromSlash(emdashInstallPath)), appImageURL}}
	if !reflect.DeepEqual(calls, want) {
		t.Fatalf("calls = %#v, want %#v", calls, want)
	}
}

func TestLaunchRunsEmdashWithExtras(t *testing.T) {
	var got []string
	sandbox := tooltest.NewSandbox("/toby/context")
	sandbox.ExecFunc = func(_ context.Context, argv []string, _ tool.ExecOptions) (int, error) {
		got = append([]string(nil), argv...)
		return 0, nil
	}
	svc := Provide(Params{Sandbox: sandbox, ContextFiles: contextfiles.NewService()}).Service

	if err := svc.Launch(context.Background(), []string{"--open", "project"}); err != nil {
		t.Fatal(err)
	}
	want := []string{"emdash", "--open", "project"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("argv = %#v, want %#v", got, want)
	}
}
