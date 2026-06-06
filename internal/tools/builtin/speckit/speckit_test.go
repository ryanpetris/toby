package speckit

import (
	"context"
	"io"
	"net/http"
	"reflect"
	"strings"
	"testing"

	"petris.dev/toby/internal/tools/builtin/uv"
	"petris.dev/toby/internal/tools/fake"
	sandboxapi "petris.dev/toby/sandbox"
	"petris.dev/toby/tools"
)

func TestLatestReleaseTagTrimsTag(t *testing.T) {
	svc := &speckitTool{http: speckitHTTPClient(http.StatusOK, `{"tag_name":" v0.5.0 "}`)}

	tag, err := svc.latestReleaseTag(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if tag != "v0.5.0" {
		t.Fatalf("tag = %q", tag)
	}
}

func TestLatestReleaseTagRejectsMissingTag(t *testing.T) {
	svc := &speckitTool{http: speckitHTTPClient(http.StatusOK, `{}`)}

	_, err := svc.latestReleaseTag(context.Background())
	if err == nil || !strings.Contains(err.Error(), "missing tag_name") {
		t.Fatalf("err = %v", err)
	}
}

func TestInstallSkipsWhenSpecifyExists(t *testing.T) {
	sandbox := fake.NewSandbox("/toby/context")
	svc := &speckitTool{Base: tools.Base{Metadata: tools.Metadata{Name: Name}}, sandbox: sandbox}
	var execCalls [][]string
	sandbox.ExecFunc = func(_ context.Context, argv []string, _ sandboxapi.ExecOptions) (int, error) {
		execCalls = append(execCalls, append([]string(nil), argv...))
		return 0, nil
	}

	if err := svc.Install(context.Background(), false); err != nil {
		t.Fatal(err)
	}
	if want := [][]string{{"which", "specify"}}; !reflect.DeepEqual(execCalls, want) {
		t.Fatalf("exec calls = %#v, want %#v", execCalls, want)
	}
}

func TestUpgradeRunsUVToolInstallWithLatestTag(t *testing.T) {
	sandbox := fake.NewSandbox("/toby/context")
	svc := &speckitTool{
		Base:    tools.Base{Metadata: tools.Metadata{Name: Name}},
		http:    speckitHTTPClient(http.StatusOK, `{"tag_name":"v0.5.0"}`),
		sandbox: sandbox,
	}
	var execCalls [][]string
	sandbox.ExecFunc = func(_ context.Context, argv []string, _ sandboxapi.ExecOptions) (int, error) {
		execCalls = append(execCalls, append([]string(nil), argv...))
		return 0, nil
	}

	if err := svc.Install(context.Background(), true); err != nil {
		t.Fatal(err)
	}
	wantCommand := []string{"uv", "tool", "install", "specify-cli", "--force", "--from", "git+" + repositoryURL + "@v0.5.0"}
	if want := [][]string{wantCommand}; !reflect.DeepEqual(execCalls, want) {
		t.Fatalf("exec calls = %#v, want %#v", execCalls, want)
	}
}

func TestSpeckitDeclaresUVDependency(t *testing.T) {
	svc := Provide(Params{HTTP: http.DefaultClient, Sandbox: fake.NewSandbox("/toby/context")}).Service
	if got := svc.Dependencies(); len(got) != 1 || got[0] != uv.Name {
		t.Fatalf("dependency metadata = deps %#v", got)
	}
}

func TestLaunchRunsSpecifyWithExtras(t *testing.T) {
	sandbox := fake.NewSandbox("/toby/context")
	svc := &speckitTool{Base: tools.Base{Metadata: tools.Metadata{Name: Name}}, sandbox: sandbox}
	var got []string
	sandbox.ExecFunc = func(_ context.Context, argv []string, _ sandboxapi.ExecOptions) (int, error) {
		got = append([]string(nil), argv...)
		return 0, nil
	}

	if err := svc.Launch(context.Background(), []string{"init", "feature"}); err != nil {
		t.Fatal(err)
	}
	want := []string{"specify", "init", "feature"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("argv = %#v, want %#v", got, want)
	}
}

type speckitRoundTripFunc func(*http.Request) (*http.Response, error)

func (f speckitRoundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) { return f(req) }

func speckitHTTPClient(status int, body string) *http.Client {
	return &http.Client{Transport: speckitRoundTripFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: status, Status: http.StatusText(status), Body: io.NopCloser(strings.NewReader(body)), Header: make(http.Header)}, nil
	})}
}
