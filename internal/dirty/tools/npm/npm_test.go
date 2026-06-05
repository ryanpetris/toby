package npm

import (
	"bytes"
	"context"
	"path/filepath"
	"petris.dev/toby/container/layout"
	"reflect"
	"testing"

	"petris.dev/toby/config"
	contextfiles "petris.dev/toby/context/files"
	"petris.dev/toby/internal/dirty/tools/tooltest"
	sandboxapi "petris.dev/toby/sandbox"
	"petris.dev/toby/tools"
)

func TestRegisterContextFilesWritesSandboxInit(t *testing.T) {
	sandbox := tooltest.NewSandbox(filepath.Join(t.TempDir(), "context"))
	contextFiles := contextfiles.NewService()
	contextFiles.SetSandbox(sandbox)
	svc := newTestNPM(t, sandbox, contextFiles).(tools.ContextFileRegistrar)

	if err := svc.RegisterContextFiles(context.Background(), tools.ContextOptions{}); err != nil {
		t.Fatal(err)
	}

	files := sandbox.Files
	if len(files) != 1 {
		t.Fatalf("files = %#v", files)
	}
	file := files[0]
	if file.Path != npmSandboxInitPath || file.Mode != 0o755 {
		t.Fatalf("file = %#v", file)
	}
	for _, want := range [][]byte{[]byte("#!/bin/sh"), []byte("command -v npm"), []byte("mkdir -p")} {
		if !bytes.Contains(file.Data, want) {
			t.Fatalf("sandbox init missing %q:\n%s", want, file.Data)
		}
	}
}

func TestSandboxInitRunsContextScript(t *testing.T) {
	contextDir := filepath.Join(t.TempDir(), "context")
	var got []string
	sandbox := tooltest.NewSandbox(contextDir)
	sandbox.ExecFunc = func(_ context.Context, argv []string, _ sandboxapi.ExecOptions) (int, error) {
		got = append([]string(nil), argv...)
		return 0, nil
	}
	svc := newTestNPM(t, sandbox, contextfiles.NewService())

	if err := svc.InitSandbox(context.Background()); err != nil {
		t.Fatal(err)
	}

	want := []string{filepath.Join(layout.Context, filepath.FromSlash(npmSandboxInitPath))}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("argv = %#v, want %#v", got, want)
	}
}

func newTestNPM(t *testing.T, sandbox sandboxapi.Service, contextFiles *contextfiles.Service) tools.Tool {
	t.Helper()
	home := t.TempDir()
	return Provide(Params{Paths: config.Paths{Home: home, SandboxRoot: filepath.Join(home, "sandboxes")}, Sandbox: sandbox, ContextFiles: contextFiles}).Service
}
