package forgejocli

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"path/filepath"
	"reflect"
	"testing"

	contextfiles "petris.dev/toby/context/files"
	sandboxapi "petris.dev/toby/sandbox"
	"petris.dev/toby/tools"
	"petris.dev/toby/tools/fake"
	"petris.dev/toby/tools/kit"
)

func TestProvideMetadataAndLaunch(t *testing.T) {
	sandbox := fake.NewSandbox("/toby/context")
	svc := Provide(Params{Sandbox: sandbox, ContextFiles: contextfiles.NewService()}).Service
	if svc.Name() != Name || svc.CommandName() != "fj" || svc.LaunchHelp() == "" {
		t.Fatalf("metadata = name %q command %q help %q", svc.Name(), svc.CommandName(), svc.LaunchHelp())
	}
	var got []string
	sandbox.ExecFunc = func(_ context.Context, argv []string, _ sandboxapi.ExecOptions) (int, error) {
		got = append([]string(nil), argv...)
		return 0, nil
	}

	if err := svc.Launch(context.Background(), []string{"repo", "list"}); err != nil {
		t.Fatal(err)
	}
	if want := []string{"fj", "repo", "list"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("argv = %#v, want %#v", got, want)
	}
}

func TestRegisterContextFilesWritesInstaller(t *testing.T) {
	sandbox := fake.NewSandbox(filepath.Join(t.TempDir(), "context"))
	contextFiles := contextfiles.NewService()
	contextFiles.SetSandbox(sandbox)
	svc := Provide(Params{Sandbox: sandbox, ContextFiles: contextFiles}).Service.(tools.ContextFileRegistrar)

	if err := svc.RegisterContextFiles(context.Background(), tools.ContextOptions{}); err != nil {
		t.Fatal(err)
	}
	files := sandbox.Files
	if len(files) != 1 || files[0].Path != forgejoCLIInstallPath || files[0].Mode != 0o755 {
		t.Fatalf("files = %#v", files)
	}
	if !bytes.Contains(files[0].Data, []byte("#!/bin/sh")) || !bytes.Contains(files[0].Data, []byte("fj")) {
		t.Fatalf("installer contents = %s", files[0].Data)
	}
}

func TestArchiveURLSelectsMatchingAsset(t *testing.T) {
	arch, err := kit.LinuxAssetArch("forgejo-cli")
	if err != nil {
		t.Skip(err)
	}
	archiveURL := "https://downloads.example.invalid/fj.tar.gz"
	body := fmt.Sprintf(`[{"assets":[{"name":%q,"browser_download_url":%q}]}]`, "forgejo-cli-"+arch+"-linux.tar.gz", archiveURL)
	svc := &forgejoCLITool{http: forgejoHTTPClient(http.StatusOK, body)}

	got, err := svc.archiveURL(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if got != archiveURL {
		t.Fatalf("archiveURL = %q, want %q", got, archiveURL)
	}
}

type forgejoRoundTripFunc func(*http.Request) (*http.Response, error)

func (f forgejoRoundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) { return f(req) }

func forgejoHTTPClient(status int, body string) *http.Client {
	return &http.Client{Transport: forgejoRoundTripFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: status, Status: http.StatusText(status), Body: io.NopCloser(bytes.NewBufferString(body)), Header: make(http.Header)}, nil
	})}
}
