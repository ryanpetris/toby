package gitlabcli

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"path/filepath"
	"reflect"
	"testing"

	contextfiles "petris.dev/toby/internal/context/files"
	"petris.dev/toby/internal/tools/tool"
	"petris.dev/toby/internal/tools/tooltest"
	"petris.dev/toby/internal/tools/toolutil"
)

func TestProvideMetadataAndLaunch(t *testing.T) {
	sandbox := tooltest.NewSandbox("/toby/context")
	svc := Provide(Params{Sandbox: sandbox, ContextFiles: contextfiles.NewService()}).Service
	if svc.Name() != tool.GitLabCliToolName || svc.CommandName() != "glab" || svc.LaunchHelp() == "" {
		t.Fatalf("metadata = name %q command %q help %q", svc.Name(), svc.CommandName(), svc.LaunchHelp())
	}
	var got []string
	sandbox.ExecFunc = func(_ context.Context, argv []string, _ tool.ExecOptions) (int, error) {
		got = append([]string(nil), argv...)
		return 0, nil
	}

	if err := svc.Launch(context.Background(), []string{"mr", "list"}); err != nil {
		t.Fatal(err)
	}
	if want := []string{"glab", "mr", "list"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("argv = %#v, want %#v", got, want)
	}
}

func TestRegisterContextFilesWritesInstaller(t *testing.T) {
	sandbox := tooltest.NewSandbox(filepath.Join(t.TempDir(), "context"))
	contextFiles := contextfiles.NewService()
	contextFiles.SetSandbox(sandbox)
	svc := Provide(Params{Sandbox: sandbox, ContextFiles: contextFiles}).Service.(tool.ContextFileTool)

	if err := svc.RegisterContextFiles(context.Background(), tool.ContextOptions{}); err != nil {
		t.Fatal(err)
	}
	files := sandbox.Files
	if len(files) != 1 || files[0].Path != gitlabCLIInstallPath || files[0].Mode != 0o500 {
		t.Fatalf("files = %#v", files)
	}
	if !bytes.Contains(files[0].Data, []byte("#!/bin/sh")) || !bytes.Contains(files[0].Data, []byte("glab")) {
		t.Fatalf("installer contents = %s", files[0].Data)
	}
}

func TestArchiveURLSelectsMatchingLink(t *testing.T) {
	arch, err := toolutil.GoAssetArch("glab")
	if err != nil {
		t.Skip(err)
	}
	archiveURL := "https://downloads.example.invalid/glab.tar.gz"
	body := fmt.Sprintf(`{"assets":{"links":[{"name":%q,"direct_asset_url":%q}]}}`, "glab_1.0.0_linux_"+arch+".tar.gz", archiveURL)
	svc := &gitlabCLITool{http: gitlabHTTPClient(http.StatusOK, body)}

	got, err := svc.archiveURL(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if got != archiveURL {
		t.Fatalf("archiveURL = %q, want %q", got, archiveURL)
	}
}

type gitlabRoundTripFunc func(*http.Request) (*http.Response, error)

func (f gitlabRoundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) { return f(req) }

func gitlabHTTPClient(status int, body string) *http.Client {
	return &http.Client{Transport: gitlabRoundTripFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: status, Status: http.StatusText(status), Body: io.NopCloser(bytes.NewBufferString(body)), Header: make(http.Header)}, nil
	})}
}
