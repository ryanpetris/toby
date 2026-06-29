package githubcli

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"path/filepath"
	"reflect"
	"testing"

	"petris.dev/toby/container/layout"
	contextfiles "petris.dev/toby/context/files"
	"petris.dev/toby/internal/tools/fake"
	sandboxapi "petris.dev/toby/sandbox"
	"petris.dev/toby/tools"
	"petris.dev/toby/tools/kit"
)

func TestProvideMetadataAndLaunch(t *testing.T) {
	sandbox := fake.NewSandbox("/toby/context")
	svc := Provide(Params{Sandbox: sandbox, ContextFiles: contextfiles.NewService()}).Service
	if svc.Name() != Name || svc.CommandName() != "gh" || svc.LaunchHelp() == "" {
		t.Fatalf("metadata = name %q command %q help %q", svc.Name(), svc.CommandName(), svc.LaunchHelp())
	}
	var got []string
	sandbox.ExecFunc = func(_ context.Context, argv []string, _ sandboxapi.ExecOptions) (int, error) {
		got = append([]string(nil), argv...)
		return 0, nil
	}

	if err := svc.Launch(context.Background(), []string{"repo", "view"}); err != nil {
		t.Fatal(err)
	}
	if want := []string{"gh", "repo", "view"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("argv = %#v, want %#v", got, want)
	}
}

func TestConfigureSandboxAddsLocalBinToPath(t *testing.T) {
	sandbox := fake.NewSandbox("/toby/context")
	svc := Provide(Params{Sandbox: sandbox, ContextFiles: contextfiles.NewService()}).Service

	if err := svc.ConfigureSandbox(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got, want := sandbox.Env["PATH"], filepath.Join(layout.Home, ".local", "bin"); got != want {
		t.Fatalf("PATH = %q, want %q", got, want)
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
	if len(files) != 1 || files[0].Path != githubCLIInstallPath || files[0].Mode != 0o755 {
		t.Fatalf("files = %#v", files)
	}
	if !bytes.Contains(files[0].Data, []byte("#!/bin/sh")) || !bytes.Contains(files[0].Data, []byte("gh")) {
		t.Fatalf("installer contents = %s", files[0].Data)
	}
}

func TestArchiveURLSelectsMatchingAsset(t *testing.T) {
	arch, err := kit.GoAssetArch("gh")
	if err != nil {
		t.Skip(err)
	}
	archiveURL := "https://downloads.example.invalid/gh.tar.gz"
	svc := &githubCLITool{http: githubHTTPClient(http.StatusOK, fmt.Sprintf(`{"assets":[{"name":%q,"browser_download_url":%q}]}`, "gh_2.0.0_linux_"+arch+".tar.gz", archiveURL))}

	got, err := svc.archiveURL(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if got != archiveURL {
		t.Fatalf("archiveURL = %q, want %q", got, archiveURL)
	}
}

type githubRoundTripFunc func(*http.Request) (*http.Response, error)

func (f githubRoundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) { return f(req) }

func githubHTTPClient(status int, body string) *http.Client {
	return &http.Client{Transport: githubRoundTripFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: status, Status: http.StatusText(status), Body: io.NopCloser(bytes.NewBufferString(body)), Header: make(http.Header)}, nil
	})}
}
