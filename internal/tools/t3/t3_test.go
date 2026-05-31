package t3

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

type fakeNPM struct{ tool.Base }

func TestRegisterContextFilesWritesWrapper(t *testing.T) {
	svc := newTestT3(t)
	registrar, ok := svc.(tool.ContextFileTool)
	if !ok {
		t.Fatal("t3 tool should register context files")
	}
	run := &tool.RunContext{ContextFiles: contextfiles.NewService().NewSession("/tmp/toby/context")}

	if err := registrar.RegisterContextFiles(context.Background(), run); err != nil {
		t.Fatal(err)
	}

	files := run.ContextFiles.Files()
	if len(files) != 1 {
		t.Fatalf("files = %#v", files)
	}
	file := files[0]
	if file.Path != t3WrapperPath || file.Mode != 0o500 {
		t.Fatalf("file = %#v", file)
	}
	for _, want := range [][]byte{[]byte(`t3 "$@" &`), []byte(`trap terminate_child INT`), []byte(`kill -TERM "$child"`)} {
		if !bytes.Contains(file.Data, want) {
			t.Fatalf("wrapper missing %q:\n%s", want, file.Data)
		}
	}
	for _, unwanted := range [][]byte{[]byte(`command -v`)} {
		if bytes.Contains(file.Data, unwanted) {
			t.Fatalf("wrapper should not contain %q:\n%s", unwanted, file.Data)
		}
	}
}

func TestLaunchRunsContextWrapper(t *testing.T) {
	svc := newTestT3(t)
	contextDir := filepath.Join(t.TempDir(), "context")
	var got []string
	run := &tool.RunContext{
		Sandbox: fakeSandbox{contextDir: contextDir},
		Extra:   []string{"--foo", "bar"},
		Launch: func(_ context.Context, argv []string, _ tool.ExecOptions) (int, error) {
			got = append([]string(nil), argv...)
			return 0, nil
		},
	}

	if err := svc.Launch(context.Background(), run); err != nil {
		t.Fatal(err)
	}

	want := []string{filepath.Join(contextDir, filepath.FromSlash(t3WrapperPath)), "--foo", "bar"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("argv = %#v, want %#v", got, want)
	}
}

func TestProvideChecksInstalledT3Binary(t *testing.T) {
	svc := newTestT3(t).(*t3Tool)
	if svc.InstallCheckCommand != "t3" {
		t.Fatalf("InstallCheckCommand = %q, want t3", svc.InstallCheckCommand)
	}
}

func newTestT3(t *testing.T) tool.Tool {
	t.Helper()
	home := t.TempDir()
	return Provide(Params{
		Paths: config.Paths{Home: home, SandboxRoot: filepath.Join(home, "sandboxes")},
		NPM:   fakeNPM{Base: tool.Base{Metadata: tool.Metadata{Name: tool.NpmToolName}}},
	}).Service
}

type fakeSandbox struct{ contextDir string }

func (s fakeSandbox) HomeDir() string { return filepath.Dir(s.contextDir) }

func (s fakeSandbox) Projects() string { return filepath.Join(filepath.Dir(s.contextDir), "Projects") }

func (s fakeSandbox) TobyRuntimeDir() string { return filepath.Dir(s.contextDir) }

func (s fakeSandbox) TobyContextDir() string { return s.contextDir }

func (s fakeSandbox) TobyOpenCodeConfigDir() string { return filepath.Join(s.contextDir, "opencode") }
