package npm

import (
	"bytes"
	"context"
	"path/filepath"
	"reflect"
	"testing"

	"petris.dev/toby/internal/config"
	"petris.dev/toby/internal/context/files"
	"petris.dev/toby/internal/tools/tool"
)

func TestRegisterContextFilesWritesSandboxInit(t *testing.T) {
	svc := newTestNPM(t).(tool.ContextFileTool)
	run := &tool.RunContext{ContextFiles: contextfiles.NewService().NewSession(filepath.Join(t.TempDir(), "context"))}

	if err := svc.RegisterContextFiles(context.Background(), run); err != nil {
		t.Fatal(err)
	}

	files := run.ContextFiles.Files()
	if len(files) != 1 {
		t.Fatalf("files = %#v", files)
	}
	file := files[0]
	if file.Path != npmSandboxInitPath || file.Mode != 0o500 {
		t.Fatalf("file = %#v", file)
	}
	for _, want := range [][]byte{[]byte("#!/bin/sh"), []byte("command -v npm"), []byte("mkdir -p")} {
		if !bytes.Contains(file.Data, want) {
			t.Fatalf("sandbox init missing %q:\n%s", want, file.Data)
		}
	}
}

func TestSandboxInitRunsContextScript(t *testing.T) {
	svc := newTestNPM(t)
	contextDir := filepath.Join(t.TempDir(), "context")
	var got []string
	run := &tool.RunContext{
		ContextFiles: contextfiles.NewService().NewSession(contextDir),
		Exec: func(_ context.Context, argv []string, _ tool.ExecOptions) (int, error) {
			got = append([]string(nil), argv...)
			return 0, nil
		},
	}

	if err := svc.SandboxInit(context.Background(), run); err != nil {
		t.Fatal(err)
	}

	want := []string{filepath.Join(contextDir, filepath.FromSlash(npmSandboxInitPath))}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("argv = %#v, want %#v", got, want)
	}
}

func newTestNPM(t *testing.T) tool.Tool {
	t.Helper()
	home := t.TempDir()
	return Provide(config.Paths{Home: home, SandboxRoot: filepath.Join(home, "sandboxes")}).Service
}
