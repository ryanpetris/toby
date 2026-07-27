package uv

// Tests UV installation, upgrades, and launch behavior.

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	sandboxapi "petris.dev/toby/internal/sandbox"
	"petris.dev/toby/internal/sandbox/layout"
	"petris.dev/toby/internal/tools"
	"petris.dev/toby/internal/tools/fake"
)

func TestConfigureSandboxConfiguresEnvironment(t *testing.T) {
	sandbox := fake.NewSandbox()
	svc := provide(params{Sandbox: sandbox}).Service
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
	if got, want := sandbox.Env["PATH"], strings.Join([]string{filepath.Join(layout.Home, ".local", "bin"), filepath.Join(shared, "bin")}, ":"); got != want {
		t.Fatalf("PATH = %q, want %q", got, want)
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
	sandbox := fake.NewSandbox()
	sandbox.ExecFunc = func(_ context.Context, argv []string, _ sandboxapi.ExecOptions) (int, error) {
		calls = append(calls, append([]string(nil), argv...))
		return 0, nil
	}
	svc := provide(params{Sandbox: sandbox}).Service

	if err := svc.Install(context.Background(), false); err != nil {
		t.Fatal(err)
	}
	want := [][]string{{"which", "uv"}}
	if !reflect.DeepEqual(calls, want) {
		t.Fatalf("calls = %#v, want %#v", calls, want)
	}
}

func TestUpgradeRunsInstallerWithLatestArchive(t *testing.T) {
	svc := &uvTool{Base: tools.Base{Metadata: tools.Metadata{Name: Name}}}
	assetName, err := svc.assetName()
	if err != nil {
		t.Skip(err)
	}
	archiveURL := "https://downloads.example.invalid/uv.tar.gz"
	svc.http = uvHTTPClient(http.StatusOK, fmt.Sprintf(`{"tag_name":"v1.2.3","assets":[{"name":%q,"browser_download_url":%q}]}`, assetName, archiveURL))
	var calls [][]string
	var options []sandboxapi.ExecOptions
	sandbox := fake.NewSandbox()
	sandbox.ExecFunc = func(
		_ context.Context,
		argv []string,
		opts sandboxapi.ExecOptions,
	) (int, error) {
		calls = append(calls, append([]string(nil), argv...))
		options = append(options, opts)
		return 0, nil
	}
	svc.sandbox = sandbox

	if err := svc.Install(context.Background(), true); err != nil {
		t.Fatal(err)
	}
	want := [][]string{{filepath.Join(layout.Runtime, filepath.FromSlash(uvInstallPath)), archiveURL}}
	if !reflect.DeepEqual(calls, want) {
		t.Fatalf("calls = %#v, want %#v", calls, want)
	}
	if len(options) != 1 || options[0].Status != "Installing" {
		t.Fatalf("install options = %#v", options)
	}
}

func TestInitSandboxCreatesManagedDirsBeforeInstallCheck(t *testing.T) {
	sandbox := fake.NewSandbox()
	svc := provide(params{Sandbox: sandbox}).Service
	if err := svc.ConfigureSandbox(context.Background()); err != nil {
		t.Fatal(err)
	}

	shared := filepath.Join(layout.Home, ".local", "share", "toby", "uv")
	wantDirs := []string{shared, filepath.Join(shared, "tools"), filepath.Join(shared, "bin"), filepath.Join(shared, "cache")}
	var calls [][]string
	var options []sandboxapi.ExecOptions
	sandbox.ExecFunc = func(
		_ context.Context,
		argv []string,
		opts sandboxapi.ExecOptions,
	) (int, error) {
		calls = append(calls, append([]string(nil), argv...))
		options = append(options, opts)
		return 0, nil
	}

	if err := svc.InitSandbox(context.Background()); err != nil {
		t.Fatal(err)
	}
	wantMkdir := append([]string{"mkdir", "-p", "--"}, wantDirs...)
	if len(calls) == 0 || !reflect.DeepEqual(calls[0], wantMkdir) {
		t.Fatalf("first command = %#v, want %#v", calls, wantMkdir)
	}
	if len(options) == 0 || options[0].Status != "Preparing storage" {
		t.Fatalf("first command options = %#v", options)
	}
}

func TestLaunchRunsUVWithExtras(t *testing.T) {
	var got []string
	sandbox := fake.NewSandbox()
	sandbox.ExecFunc = func(_ context.Context, argv []string, _ sandboxapi.ExecOptions) (int, error) {
		got = append([]string(nil), argv...)
		return 0, nil
	}
	svc := provide(params{Sandbox: sandbox}).Service

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
