package codex

// Tests Codex launch arguments and runtime-specific configuration behavior.

import (
	"context"
	"reflect"
	"slices"
	"strings"
	"testing"

	appconfig "petris.dev/toby/internal/config/app"
	"petris.dev/toby/internal/config/session"
	sandboxapi "petris.dev/toby/internal/sandbox"
	"petris.dev/toby/internal/tools"
	"petris.dev/toby/internal/tools/fake"
)

func TestLaunchAddsTobyConfigOverrides(t *testing.T) {
	cdx, sandbox, holder := newTestCodex(t, testConfig(t, false))
	holder.Set(sessionconfig.Config{
		MCPServers: []sessionconfig.MCPServer{{
			Name:      "toby",
			Transport: sessionconfig.MCPTransportHTTP,
			URL:       "http://127.0.0.1:12345/mcp/toby",
		}},
		Instructions: sessionconfig.Instructions{Contents: [][]byte{[]byte("# user instructions\n")}},
	})
	var got []string
	sandbox.ExecFunc = func(_ context.Context, argv []string, _ sandboxapi.ExecOptions) (int, error) {
		got = append([]string(nil), argv...)
		return 0, nil
	}

	if err := cdx.Launch(context.Background(), []string{"--model", "gpt-5"}); err != nil {
		t.Fatal(err)
	}
	want := []string{
		"codex",
		"-c", `mcp_servers={'toby' = {enabled = true, url = 'http://127.0.0.1:12345/mcp/toby'}}`,
		"--model", "gpt-5",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("argv = %#v, want %#v", got, want)
	}
}

func TestLaunchYoloBypassesApprovals(t *testing.T) {
	cdx, sandbox, _ := newTestCodex(t, testConfig(t, true))
	var got []string
	sandbox.ExecFunc = func(_ context.Context, argv []string, _ sandboxapi.ExecOptions) (int, error) {
		got = append([]string(nil), argv...)
		return 0, nil
	}

	if err := cdx.PrepareHost(context.Background(), &tools.Options{}); err != nil {
		t.Fatal(err)
	}
	if err := cdx.Launch(context.Background(), []string{"--model", "gpt-5"}); err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(got, "--dangerously-bypass-approvals-and-sandbox") {
		t.Fatalf("argv = %#v, missing --dangerously-bypass-approvals-and-sandbox", got)
	}

	got = nil
	plain, plainSandbox, _ := newTestCodex(t, testConfig(t, false))
	plainSandbox.ExecFunc = func(_ context.Context, argv []string, _ sandboxapi.ExecOptions) (int, error) {
		got = append([]string(nil), argv...)
		return 0, nil
	}
	if err := plain.PrepareHost(context.Background(), &tools.Options{}); err != nil {
		t.Fatal(err)
	}
	if err := plain.Launch(context.Background(), nil); err != nil {
		t.Fatal(err)
	}
	if slices.Contains(got, "--dangerously-bypass-approvals-and-sandbox") {
		t.Fatalf("argv = %#v, unexpected --dangerously-bypass-approvals-and-sandbox", got)
	}
}

func TestNativeLaunchKeepsHighestPriorityMCPOverrides(t *testing.T) {
	recorder := fake.NewSandbox()
	native := &nativeCodexSandbox{Sandbox: recorder}
	holder := sessionconfig.NewHolder()
	holder.Set(sessionconfig.Config{
		MCPServers: []sessionconfig.MCPServer{{
			Name:      "toby",
			Transport: sessionconfig.MCPTransportStdio,
			Command:   "/toby/bin/tobys",
			Args:      []string{"resource", "connect", "--", "toby"},
		}},
		Instructions: sessionconfig.Instructions{
			Contents: [][]byte{[]byte("# native instructions\n")},
		},
	})
	tool := provide(params{
		Sandbox:       native,
		SessionConfig: holder,
		Config:        testConfig(t, false),
	}).Service

	var got []string
	recorder.ExecFunc = func(
		_ context.Context,
		argv []string,
		_ sandboxapi.ExecOptions,
	) (int, error) {
		got = append([]string(nil), argv...)
		return 0, nil
	}

	if err := tool.Launch(t.Context(), []string{"--model", "gpt-5"}); err != nil {
		t.Fatal(err)
	}
	want := []string{
		"codex",
		"-c", `mcp_servers={'toby' = {args = ['resource', 'connect', '--', 'toby'], command = '/toby/bin/tobys', enabled = true}}`,
		"--model", "gpt-5",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("native argv = %#v", got)
	}
	for _, arg := range got {
		if strings.Contains(arg, "developer_instructions") {
			t.Fatalf("native argv contains an instruction override: %#v", got)
		}
	}
}

func newTestCodex(t *testing.T, cfg *appconfig.LaunchHolder) (tools.Tool, *fake.Sandbox, *sessionconfig.Holder) {
	t.Helper()
	sandbox := fake.NewSandbox()
	holder := sessionconfig.NewHolder()
	holder.Set(sessionconfig.Config{})
	tool := provide(params{Sandbox: sandbox, SessionConfig: holder, Config: cfg}).Service
	return tool, sandbox, holder
}

// testConfig builds an appconfig.Service for tests, optionally with yolo folded in.
func testConfig(t *testing.T, yolo bool) *appconfig.LaunchHolder {
	t.Helper()
	base, err := appconfig.Load(t.TempDir(), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if !yolo {
		return appconfig.NewLaunchHolder(base)
	}
	enabled := true
	return appconfig.NewLaunchHolder(
		base.WithOverrides(appconfig.LaunchOverrides{Yolo: &enabled}),
	)
}

type nativeCodexSandbox struct {
	*fake.Sandbox
}

func (*nativeCodexSandbox) UsesNativeToolFiles() bool {
	return true
}
