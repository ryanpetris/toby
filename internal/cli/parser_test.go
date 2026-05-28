package cli

import (
	"reflect"
	"testing"

	"petris.dev/toby/internal/tool"
)

type contextTool struct{ tool.Base }

func TestParseSandboxArgsLaunch(t *testing.T) {
	ctxTools := []tool.Tool{
		contextTool{Base: tool.Base{Metadata: tool.Metadata{Name: tool.NpmToolName, LaunchHelp: "Launch Node Package Manager"}}},
		contextTool{Base: tool.Base{Metadata: tool.Metadata{Name: tool.GitHubCliToolName, CLIName: "gh", LaunchHelp: "Launch GitHub CLI"}}},
	}
	parsed, err := parseSandboxArgs(
		[]string{"--tmp-env", "proj", "--with-gh", "--upgrade", "--", "--repo", "x"},
		true,
		tool.OpenCodeToolName,
		ctxTools,
		nil,
	)
	if err != nil {
		t.Fatal(err)
	}
	if !parsed.Options.TmpEnv || parsed.Options.Env != "proj" || !parsed.Options.Upgrade {
		t.Fatalf("parsed options = %#v", parsed.Options)
	}
	if got, want := parsed.RequestedTools, []string{tool.GitHubCliToolName, tool.OpenCodeToolName}; !reflect.DeepEqual(got, want) {
		t.Fatalf("requested = %#v, want %#v", got, want)
	}
	if got, want := parsed.Extra, []string{"--repo", "x"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("extra = %#v, want %#v", got, want)
	}
}

func TestParseSandboxArgsOpenCodeSyncModels(t *testing.T) {
	opts := &tool.CommandOptions{}
	parsed, err := parseSandboxArgs(
		[]string{"env", "--sync-models", "run"},
		true,
		tool.OpenCodeToolName,
		nil,
		opencodeArgParser(opts),
	)
	if err != nil {
		t.Fatal(err)
	}
	parsed.Options.SyncModels = opts.SyncModels
	if !parsed.Options.SyncModels {
		t.Fatal("expected sync models flag")
	}
	if got, want := parsed.Extra, []string{"run"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("extra = %#v, want %#v", got, want)
	}
}
