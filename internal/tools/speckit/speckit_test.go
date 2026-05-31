package speckit

import (
	"context"
	"errors"
	"io"
	"net/http"
	"reflect"
	"strings"
	"testing"

	"petris.dev/toby/internal/tools/tool"
)

type fakeUV struct {
	tool.Base
	binds   []tool.Bind
	entries []tool.PathTarget
	calls   *[]string
	err     error
}

func (t fakeUV) Binds() []tool.Bind { return append([]tool.Bind(nil), t.binds...) }

func (t fakeUV) PathEntries() []tool.PathTarget {
	return append([]tool.PathTarget(nil), t.entries...)
}

func (t fakeUV) Install(context.Context, *tool.RunContext) error {
	*t.calls = append(*t.calls, "install:"+t.Name())
	return t.err
}

func (t fakeUV) Upgrade(context.Context, *tool.RunContext) error {
	*t.calls = append(*t.calls, "upgrade:"+t.Name())
	return t.err
}

func TestBindsAndPathEntriesDelegateToUV(t *testing.T) {
	var calls []string
	bind := tool.Bind{HostPath: "/uv", Target: tool.HomeTarget(".uv")}
	entry := tool.HomeTarget(".uv", "bin")
	svc := &speckitTool{uv: fakeUV{Base: tool.Base{Metadata: tool.Metadata{Name: tool.UvToolName}}, binds: []tool.Bind{bind}, entries: []tool.PathTarget{entry}, calls: &calls}}

	if got, want := svc.Binds(), []tool.Bind{bind}; !reflect.DeepEqual(got, want) {
		t.Fatalf("Binds = %#v, want %#v", got, want)
	}
	if got, want := svc.PathEntries(), []tool.PathTarget{entry}; !reflect.DeepEqual(got, want) {
		t.Fatalf("PathEntries = %#v, want %#v", got, want)
	}
}

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

func TestInstallRunsUVDependencyAndSkipsWhenSpecifyExists(t *testing.T) {
	var depCalls []string
	svc := &speckitTool{Base: tool.Base{Metadata: tool.Metadata{Name: tool.SpeckitToolName}}, uv: fakeUV{Base: tool.Base{Metadata: tool.Metadata{Name: tool.UvToolName}}, calls: &depCalls}}
	var execCalls [][]string
	run := &tool.RunContext{Exec: func(_ context.Context, argv []string, _ tool.ExecOptions) (int, error) {
		execCalls = append(execCalls, append([]string(nil), argv...))
		return 0, nil
	}}

	if err := svc.Install(context.Background(), run); err != nil {
		t.Fatal(err)
	}
	if want := []string{"install:uv"}; !reflect.DeepEqual(depCalls, want) {
		t.Fatalf("dependency calls = %#v, want %#v", depCalls, want)
	}
	if want := [][]string{{"which", "specify"}}; !reflect.DeepEqual(execCalls, want) {
		t.Fatalf("exec calls = %#v, want %#v", execCalls, want)
	}
}

func TestUpgradeRunsUVToolInstallWithLatestTag(t *testing.T) {
	var depCalls []string
	svc := &speckitTool{
		Base: tool.Base{Metadata: tool.Metadata{Name: tool.SpeckitToolName}},
		http: speckitHTTPClient(http.StatusOK, `{"tag_name":"v0.5.0"}`),
		uv:   fakeUV{Base: tool.Base{Metadata: tool.Metadata{Name: tool.UvToolName}}, calls: &depCalls},
	}
	var execCalls [][]string
	run := &tool.RunContext{Exec: func(_ context.Context, argv []string, _ tool.ExecOptions) (int, error) {
		execCalls = append(execCalls, append([]string(nil), argv...))
		return 0, nil
	}}

	if err := svc.Upgrade(context.Background(), run); err != nil {
		t.Fatal(err)
	}
	if want := []string{"upgrade:uv"}; !reflect.DeepEqual(depCalls, want) {
		t.Fatalf("dependency calls = %#v, want %#v", depCalls, want)
	}
	wantCommand := []string{"uv", "tool", "install", "specify-cli", "--force", "--from", "git+" + repositoryURL + "@v0.5.0"}
	if want := [][]string{wantCommand}; !reflect.DeepEqual(execCalls, want) {
		t.Fatalf("exec calls = %#v, want %#v", execCalls, want)
	}
}

func TestInstallStopsWhenUVDependencyFails(t *testing.T) {
	boom := errors.New("boom")
	var depCalls []string
	svc := &speckitTool{Base: tool.Base{Metadata: tool.Metadata{Name: tool.SpeckitToolName}}, uv: fakeUV{Base: tool.Base{Metadata: tool.Metadata{Name: tool.UvToolName}}, calls: &depCalls, err: boom}}
	run := &tool.RunContext{Exec: func(context.Context, []string, tool.ExecOptions) (int, error) {
		t.Fatal("executor should not run")
		return 0, nil
	}}

	if err := svc.Install(context.Background(), run); !errors.Is(err, boom) {
		t.Fatalf("err = %v, want boom", err)
	}
}

func TestLaunchRunsSpecifyWithExtras(t *testing.T) {
	svc := &speckitTool{Base: tool.Base{Metadata: tool.Metadata{Name: tool.SpeckitToolName}}}
	var got []string
	run := &tool.RunContext{Extra: []string{"init", "feature"}, Launch: func(_ context.Context, argv []string, _ tool.ExecOptions) (int, error) {
		got = append([]string(nil), argv...)
		return 0, nil
	}}

	if err := svc.Launch(context.Background(), run); err != nil {
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
