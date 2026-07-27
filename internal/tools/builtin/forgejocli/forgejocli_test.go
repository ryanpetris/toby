package forgejocli

// Tests Forgejo CLI installation and launch behavior.

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"path/filepath"
	"reflect"
	"testing"

	sandboxapi "petris.dev/toby/internal/sandbox"
	"petris.dev/toby/internal/sandbox/layout"
	"petris.dev/toby/internal/tools/fake"
	"petris.dev/toby/internal/tools/kit"
)

func TestProvideMetadataAndLaunch(t *testing.T) {
	sandbox := fake.NewSandbox()
	svc := provide(params{Sandbox: sandbox}).Service
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

func TestConfigureSandboxAddsLocalBinToPath(t *testing.T) {
	sandbox := fake.NewSandbox()
	svc := provide(params{Sandbox: sandbox}).Service

	if err := svc.ConfigureSandbox(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got, want := sandbox.Env["PATH"], filepath.Join(layout.Home, ".local", "bin"); got != want {
		t.Fatalf("PATH = %q, want %q", got, want)
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
