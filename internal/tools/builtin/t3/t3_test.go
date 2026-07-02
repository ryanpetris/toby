package t3

import (
	"bytes"
	"context"
	"path/filepath"
	"reflect"
	"testing"

	contextfiles "petris.dev/toby/context/files"
	"petris.dev/toby/internal/tools/fake"
	"petris.dev/toby/tools"
)

func TestRegisterContextFilesWritesWrapper(t *testing.T) {
	svc, sandbox := newTestT3(t, "/toby/context")

	if err := svc.RegisterContextFiles(context.Background(), tools.ContextOptions{}); err != nil {
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
	svc, _ := newTestT3(t, contextDir)

	got, err := svc.LaunchCommand(context.Background(), []string{"--foo", "bar"})
	if err != nil {
		t.Fatal(err)
	}

	want := []string{t3WrapperPath, "--foo", "bar"}
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

func newTestT3(t *testing.T, contextDir string) (*t3Tool, *fake.Sandbox) {
	t.Helper()
	sandbox := fake.NewSandbox(contextDir)
	contextFiles := contextfiles.NewService()
	contextFiles.SetSandbox(sandbox)
	return Provide(Params{Sandbox: sandbox, ContextFiles: contextFiles}).Service.(*t3Tool), sandbox
}
