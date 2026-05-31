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

	"petris.dev/toby/internal/config"
	"petris.dev/toby/internal/context/files"
	"petris.dev/toby/internal/tools/tool"
)

func TestPathEntriesAndSandboxContextSetup(t *testing.T) {
	home := t.TempDir()
	svc := Provide(config.Paths{}, nil).Service
	if got, want := svc.PathEntries(), []tool.PathTarget{tool.HomeTarget(".local", "share", "toby", "uv", "bin")}; !reflect.DeepEqual(got, want) {
		t.Fatalf("PathEntries = %#v, want %#v", got, want)
	}
	run := &tool.RunContext{Sandbox: fakeSandbox{home: home}, Env: tool.Environment{}}

	if err := svc.SandboxContextSetup(run); err != nil {
		t.Fatal(err)
	}
	shared := filepath.Join(home, ".local", "share", "toby", "uv")
	wantEnv := map[string]string{
		"UV_TOOL_DIR":     filepath.Join(shared, "tools"),
		"UV_TOOL_BIN_DIR": filepath.Join(shared, "bin"),
		"UV_CACHE_DIR":    filepath.Join(shared, "cache"),
	}
	for key, want := range wantEnv {
		if run.Env[key] != want {
			t.Fatalf("%s = %q, want %q", key, run.Env[key], want)
		}
	}
}

func TestRegisterContextFilesWritesInstaller(t *testing.T) {
	svc := Provide(config.Paths{}, nil).Service.(tool.ContextFileTool)
	run := &tool.RunContext{ContextFiles: contextfiles.NewService().NewSession(filepath.Join(t.TempDir(), "context"))}

	if err := svc.RegisterContextFiles(context.Background(), run); err != nil {
		t.Fatal(err)
	}
	files := run.ContextFiles.Files()
	if len(files) != 1 || files[0].Path != uvInstallPath || files[0].Mode != 0o500 {
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
	svc := Provide(config.Paths{}, nil).Service
	var calls [][]string
	run := &tool.RunContext{Exec: func(_ context.Context, argv []string, _ tool.ExecOptions) (int, error) {
		calls = append(calls, append([]string(nil), argv...))
		return 0, nil
	}}

	if err := svc.Install(context.Background(), run); err != nil {
		t.Fatal(err)
	}
	want := [][]string{{"which", "uv"}}
	if !reflect.DeepEqual(calls, want) {
		t.Fatalf("calls = %#v, want %#v", calls, want)
	}
}

func TestUpgradeRunsInstallerWithLatestArchive(t *testing.T) {
	svc := &uvTool{Base: tool.Base{Metadata: tool.Metadata{Name: tool.UvToolName}}}
	assetName, err := svc.assetName()
	if err != nil {
		t.Skip(err)
	}
	archiveURL := "https://downloads.example.invalid/uv.tar.gz"
	svc.http = uvHTTPClient(http.StatusOK, fmt.Sprintf(`{"tag_name":"v1.2.3","assets":[{"name":%q,"browser_download_url":%q}]}`, assetName, archiveURL))
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
	want := [][]string{{filepath.Join(contextDir, filepath.FromSlash(uvInstallPath)), archiveURL}}
	if !reflect.DeepEqual(calls, want) {
		t.Fatalf("calls = %#v, want %#v", calls, want)
	}
}

func TestLaunchRunsUVWithExtras(t *testing.T) {
	svc := Provide(config.Paths{}, nil).Service
	var got []string
	run := &tool.RunContext{Extra: []string{"tool", "list"}, Launch: func(_ context.Context, argv []string, _ tool.ExecOptions) (int, error) {
		got = append([]string(nil), argv...)
		return 0, nil
	}}

	if err := svc.Launch(context.Background(), run); err != nil {
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

type fakeSandbox struct {
	home       string
	contextDir string
}

func (s fakeSandbox) HomeDir() string {
	if s.home != "" {
		return s.home
	}
	return filepath.Dir(s.contextDir)
}

func (s fakeSandbox) Projects() string              { return filepath.Join(s.HomeDir(), "Projects") }
func (s fakeSandbox) TobyRuntimeDir() string        { return filepath.Dir(s.contextDir) }
func (s fakeSandbox) TobyContextDir() string        { return s.contextDir }
func (s fakeSandbox) TobyOpenCodeConfigDir() string { return filepath.Join(s.contextDir, "opencode") }
