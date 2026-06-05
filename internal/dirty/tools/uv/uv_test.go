package uv

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"petris.dev/toby/container/layout"
	contextfiles "petris.dev/toby/context/files"
	sandboxapi "petris.dev/toby/sandbox"
	"petris.dev/toby/tools"
	"petris.dev/toby/tools/tooltest"
)

func TestSandboxContextSetupConfiguresEnvironment(t *testing.T) {
	home := t.TempDir()
	sandbox := tooltest.NewSandbox(filepath.Join(home, "runtime", "context"))
	svc := Provide(Params{Sandbox: sandbox, ContextFiles: contextfiles.NewService()}).Service
	if err := svc.ConfigureSandbox(context.Background()); err != nil {
		t.Fatal(err)
	}
	shared := filepath.Join(layout.Home, ".local", "share", "toby", "uv")
	wantEnv := map[string]string{
		"UV_TOOL_DIR":     filepath.Join(shared, "tools"),
		"UV_TOOL_BIN_DIR": filepath.Join(shared, "bin"),
		"UV_CACHE_DIR":    filepath.Join(shared, "cache"),
	}
	for key, want := range wantEnv {
		if sandbox.Env[key] != want {
			t.Fatalf("%s = %q, want %q", key, sandbox.Env[key], want)
		}
	}
	if got, want := sandbox.Env["PATH"], filepath.Join(shared, "bin"); got != want {
		t.Fatalf("PATH = %q, want %q", got, want)
	}
}

func TestRegisterContextFilesWritesInstaller(t *testing.T) {
	sandbox := tooltest.NewSandbox(filepath.Join(t.TempDir(), "context"))
	contextFiles := contextfiles.NewService()
	contextFiles.SetSandbox(sandbox)
	svc := Provide(Params{Sandbox: sandbox, ContextFiles: contextFiles}).Service.(tools.ContextFileRegistrar)

	if err := svc.RegisterContextFiles(context.Background(), tools.ContextOptions{}); err != nil {
		t.Fatal(err)
	}
	files := sandbox.Files
	if len(files) != 1 || files[0].Path != uvInstallPath || files[0].Mode != 0o755 {
		t.Fatalf("files = %#v", files)
	}
	if !bytes.Contains(files[0].Data, []byte("#!/bin/sh")) || !bytes.Contains(files[0].Data, []byte("uvx")) {
		t.Fatalf("installer contents = %s", files[0].Data)
	}
}

func TestLatestReleaseSelectsMatchingAsset(t *testing.T) {
	svc := &uvTool{}
	assetName, err := svc.assetName()
	if err != nil {
		t.Skip(err)
	}
	archiveURL := "https://downloads.example.invalid/uv.tar.gz"
	svc.http = uvHTTPClient(http.StatusOK, fmt.Sprintf(`{"tag_name":" v1.2.3 ","assets":[{"name":%q,"browser_download_url":%q}]}`, assetName, archiveURL))

	tag, url, err := svc.latestRelease(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if tag != "v1.2.3" || url != archiveURL {
		t.Fatalf("release = tag %q url %q", tag, url)
	}
}

func TestLatestReleaseRejectsMissingTag(t *testing.T) {
	svc := &uvTool{}
	assetName, err := svc.assetName()
	if err != nil {
		t.Skip(err)
	}
	svc.http = uvHTTPClient(http.StatusOK, fmt.Sprintf(`{"assets":[{"name":%q,"browser_download_url":"https://example.invalid/uv.tar.gz"}]}`, assetName))

	_, _, err = svc.latestRelease(context.Background())
	if err == nil || !strings.Contains(err.Error(), "missing tag_name") {
		t.Fatalf("err = %v", err)
	}
}

func TestInstallSkipsWhenBinaryExists(t *testing.T) {
	var calls [][]string
	sandbox := tooltest.NewSandbox(filepath.Join(t.TempDir(), "context"))
	sandbox.ExecFunc = func(_ context.Context, argv []string, _ sandboxapi.ExecOptions) (int, error) {
		calls = append(calls, append([]string(nil), argv...))
		return 0, nil
	}
	svc := Provide(Params{Sandbox: sandbox, ContextFiles: contextfiles.NewService()}).Service

	if err := svc.Install(context.Background(), false); err != nil {
		t.Fatal(err)
	}
	want := [][]string{{"which", "uv"}}
	if !reflect.DeepEqual(calls, want) {
		t.Fatalf("calls = %#v, want %#v", calls, want)
	}
}

func TestUpgradeRunsInstallerWithLatestArchive(t *testing.T) {
	svc := &uvTool{Base: tools.Base{Metadata: tools.Metadata{Name: tools.UvToolName}}}
	assetName, err := svc.assetName()
	if err != nil {
		t.Skip(err)
	}
	archiveURL := "https://downloads.example.invalid/uv.tar.gz"
	svc.http = uvHTTPClient(http.StatusOK, fmt.Sprintf(`{"tag_name":"v1.2.3","assets":[{"name":%q,"browser_download_url":%q}]}`, assetName, archiveURL))
	contextDir := filepath.Join(t.TempDir(), "context")
	var calls [][]string
	sandbox := tooltest.NewSandbox(contextDir)
	sandbox.ExecFunc = func(_ context.Context, argv []string, _ sandboxapi.ExecOptions) (int, error) {
		calls = append(calls, append([]string(nil), argv...))
		return 0, nil
	}
	svc.sandbox = sandbox

	if err := svc.Install(context.Background(), true); err != nil {
		t.Fatal(err)
	}
	want := [][]string{{filepath.Join(layout.Context, filepath.FromSlash(uvInstallPath)), archiveURL}}
	if !reflect.DeepEqual(calls, want) {
		t.Fatalf("calls = %#v, want %#v", calls, want)
	}
}

func TestLaunchRunsUVWithExtras(t *testing.T) {
	var got []string
	sandbox := tooltest.NewSandbox("/toby/context")
	sandbox.ExecFunc = func(_ context.Context, argv []string, _ sandboxapi.ExecOptions) (int, error) {
		got = append([]string(nil), argv...)
		return 0, nil
	}
	svc := Provide(Params{Sandbox: sandbox, ContextFiles: contextfiles.NewService()}).Service

	if err := svc.Launch(context.Background(), []string{"tool", "list"}); err != nil {
		t.Fatal(err)
	}
	want := []string{"uv", "tool", "list"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("argv = %#v, want %#v", got, want)
	}
}

type uvRoundTripFunc func(*http.Request) (*http.Response, error)

func (f uvRoundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) { return f(req) }

func uvHTTPClient(status int, body string) *http.Client {
	return &http.Client{Transport: uvRoundTripFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: status, Status: http.StatusText(status), Body: io.NopCloser(strings.NewReader(body)), Header: make(http.Header)}, nil
	})}
}
