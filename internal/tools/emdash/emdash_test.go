package emdash

import (
	"bytes"
	"context"
	"path/filepath"
	"reflect"
	"testing"

	"petris.dev/toby/internal/contextfiles"
	"petris.dev/toby/internal/tool"
)

func TestRegisterContextFilesWritesInstaller(t *testing.T) {
	svc := Provide().Service.(tool.ContextFileTool)
	run := &tool.RunContext{ContextFiles: contextfiles.NewService().NewSession(filepath.Join(t.TempDir(), "context"))}

	if err := svc.RegisterContextFiles(context.Background(), run); err != nil {
		t.Fatal(err)
	}
	files := run.ContextFiles.Files()
	if len(files) != 1 || files[0].Path != emdashInstallPath || files[0].Mode != 0o500 {
		t.Fatalf("files = %#v", files)
	}
	if !bytes.Contains(files[0].Data, []byte("#!/bin/sh")) || !bytes.Contains(files[0].Data, []byte("APPIMAGE_URL")) {
		t.Fatalf("installer contents = %s", files[0].Data)
	}
}

func TestRegisterContextFilesRequiresSession(t *testing.T) {
	svc := Provide().Service.(tool.ContextFileTool)
	if err := svc.RegisterContextFiles(context.Background(), nil); err == nil {
		t.Fatal("expected missing context files session to fail")
	}
}

func TestInstallLaunchPathUsesContextFilesThenSandbox(t *testing.T) {
	contextDir := filepath.Join(t.TempDir(), "context")
	path, err := emdashInstallLaunchPath(&tool.RunContext{ContextFiles: contextfiles.NewService().NewSession(contextDir)})
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join(contextDir, filepath.FromSlash(emdashInstallPath)); path != want {
		t.Fatalf("path = %q, want %q", path, want)
	}

	sandboxContextDir := filepath.Join(t.TempDir(), "fallback-context")
	path, err = emdashInstallLaunchPath(&tool.RunContext{ContextFiles: contextfiles.NewService().NewSession(""), Sandbox: fakeSandbox{contextDir: sandboxContextDir}})
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join(sandboxContextDir, filepath.FromSlash(emdashInstallPath)); path != want {
		t.Fatalf("fallback path = %q, want %q", path, want)
	}

	if _, err := emdashInstallLaunchPath(&tool.RunContext{}); err == nil {
		t.Fatal("expected missing context dir to fail")
	}
}

func TestInstallSkipsWhenBinaryExists(t *testing.T) {
	svc := Provide().Service
	var calls [][]string
	run := &tool.RunContext{Exec: func(_ context.Context, argv []string, _ tool.ExecOptions) (int, error) {
		calls = append(calls, append([]string(nil), argv...))
		return 0, nil
	}}

	if err := svc.Install(context.Background(), run); err != nil {
		t.Fatal(err)
	}
	want := [][]string{{"which", "emdash"}}
	if !reflect.DeepEqual(calls, want) {
		t.Fatalf("calls = %#v, want %#v", calls, want)
	}
}

func TestUpgradeRunsInstallerWithoutInstallCheck(t *testing.T) {
	svc := Provide().Service
	contextDir := filepath.Join(t.TempDir(), "context")
	var calls [][]string
	run := &tool.RunContext{
		ContextFiles: contextfiles.NewService().NewSession(contextDir),
		Exec: func(_ context.Context, argv []string, _ tool.ExecOptions) (int, error) {
			calls = append(calls, append([]string(nil), argv...))
			return 0, nil
		},
	}

	if err := svc.Upgrade(context.Background(), run); err != nil {
		t.Fatal(err)
	}
	want := [][]string{{filepath.Join(contextDir, filepath.FromSlash(emdashInstallPath)), appImageURL}}
	if !reflect.DeepEqual(calls, want) {
		t.Fatalf("calls = %#v, want %#v", calls, want)
	}
}

func TestLaunchRunsEmdashWithExtras(t *testing.T) {
	svc := Provide().Service
	var got []string
	run := &tool.RunContext{Extra: []string{"--open", "project"}, Launch: func(_ context.Context, argv []string, _ tool.ExecOptions) (int, error) {
		got = append([]string(nil), argv...)
		return 0, nil
	}}

	if err := svc.Launch(context.Background(), run); err != nil {
		t.Fatal(err)
	}
	want := []string{"emdash", "--open", "project"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("argv = %#v, want %#v", got, want)
	}
}

type fakeSandbox struct{ contextDir string }

func (s fakeSandbox) HomeDir() string               { return filepath.Dir(s.contextDir) }
func (s fakeSandbox) Projects() string              { return filepath.Join(filepath.Dir(s.contextDir), "Projects") }
func (s fakeSandbox) TobyRuntimeDir() string        { return filepath.Dir(s.contextDir) }
func (s fakeSandbox) TobyContextDir() string        { return s.contextDir }
func (s fakeSandbox) TobyOpenCodeConfigDir() string { return filepath.Join(s.contextDir, "opencode") }
