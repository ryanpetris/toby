package t3

import (
	"bytes"
	"context"
	"path/filepath"
	"reflect"
	"testing"

	"petris.dev/toby/internal/config"
	contextfiles "petris.dev/toby/internal/context/files"
	"petris.dev/toby/internal/tools/tool"
	"petris.dev/toby/internal/tools/tooltest"
)

type fakeNPM struct{ tool.Base }

func TestRegisterContextFilesWritesWrapper(t *testing.T) {
	svc, sandbox := newTestT3(t, "/toby/context")

	if err := svc.RegisterContextFiles(context.Background(), tool.ContextOptions{}); err != nil {
		t.Fatal(err)
	}

	files := sandbox.Files
	if len(files) != 1 {
		t.Fatalf("files = %#v", files)
	}
	file := files[0]
	if file.Path != t3WrapperPath || file.Mode != 0o755 {
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
	contextDir := filepath.Join(t.TempDir(), "context")
	var got []string
	svc, sandbox := newTestT3(t, contextDir)
	sandbox.ExecFunc = func(_ context.Context, argv []string, _ tool.ExecOptions) (int, error) {
		got = append([]string(nil), argv...)
		return 0, nil
	}

	if err := svc.Launch(context.Background(), []string{"--foo", "bar"}); err != nil {
		t.Fatal(err)
	}

	want := []string{filepath.Join(contextDir, filepath.FromSlash(t3WrapperPath)), "--foo", "bar"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("argv = %#v, want %#v", got, want)
	}
}

func TestProvideChecksInstalledT3Binary(t *testing.T) {
	svc, _ := newTestT3(t, "/toby/context")
	if svc.InstallCheckCommand != "t3" {
		t.Fatalf("InstallCheckCommand = %q, want t3", svc.InstallCheckCommand)
	}
}

func newTestT3(t *testing.T, contextDir string) (*t3Tool, *tooltest.Sandbox) {
	t.Helper()
	home := t.TempDir()
	sandbox := tooltest.NewSandbox(contextDir)
	contextFiles := contextfiles.NewService()
	contextFiles.SetSandbox(sandbox)
	return Provide(Params{Paths: config.Paths{Home: home, SandboxRoot: filepath.Join(home, "sandboxes")}, NPM: fakeNPM{Base: tool.Base{Metadata: tool.Metadata{Name: tool.NpmToolName}}}, Sandbox: sandbox, ContextFiles: contextFiles}).Service.(*t3Tool), sandbox
}
