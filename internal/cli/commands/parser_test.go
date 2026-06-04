package commands

import (
	"reflect"
	"strings"
	"testing"

	"petris.dev/toby/internal/cli/launchconfig"
	"petris.dev/toby/internal/tools/tool"

	"github.com/spf13/cobra"
)

type contextTool struct{ tool.Base }

func TestParseLaunchCommandUsesCobraFlagsAndPassthrough(t *testing.T) {
	ctxTools := []tool.Tool{
		contextTool{Base: tool.Base{Metadata: tool.Metadata{Name: tool.NpmToolName, LaunchHelp: "Launch Node Package Manager"}}},
		contextTool{Base: tool.Base{Metadata: tool.Metadata{Name: tool.GitHubCliToolName, CLIName: "gh", LaunchHelp: "Launch GitHub CLI"}}},
	}
	parsed, err := executeTestLaunchParser(t, []string{"proj", "--debug", "--yolo", "--with-gh", "--upgrade", "--runtime", "docker", "--runtime-image=node:test", "--", "foo", "--", "bar"}, ctxTools)
	if err != nil {
		t.Fatal(err)
	}
	if parsed.Options.Env != "proj" || !parsed.Options.Upgrade || parsed.Options.SandboxRuntime != "docker" || parsed.Options.Image != "node:test" {
		t.Fatalf("parsed options = %#v", parsed.Options)
	}
	if !parsed.Options.DebugEnabled() {
		t.Fatalf("debug = %#v", parsed.Options.Debug)
	}
	if !parsed.Options.YoloEnabled() {
		t.Fatalf("yolo = %#v", parsed.Options.Yolo)
	}
	if got, want := parsed.RequestedTools, []string{tool.GitHubCliToolName, tool.OpenCodeToolName}; !reflect.DeepEqual(got, want) {
		t.Fatalf("requested = %#v, want %#v", got, want)
	}
	if got, want := parsed.Extra, []string{"foo", "--", "bar"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("extra = %#v, want %#v", got, want)
	}
}

func TestParseLaunchCommandPreservesDashAfterFirstDash(t *testing.T) {
	parsed, err := executeTestLaunchParser(t, []string{"proj", "--", "--", "foo"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := parsed.Extra, []string{"--", "foo"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("extra = %#v, want %#v", got, want)
	}
}

func TestParseLaunchCommandPassesFlagsAfterDash(t *testing.T) {
	parsed, err := executeTestLaunchParser(t, []string{"proj", "--", "--project", "tool-project"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if parsed.Options.Project != "" {
		t.Fatalf("project = %q, want empty", parsed.Options.Project)
	}
	if got, want := parsed.Extra, []string{"--project", "tool-project"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("extra = %#v, want %#v", got, want)
	}
}

func TestParseLaunchCommandRejectsExtraPositionalBeforeDash(t *testing.T) {
	_, err := executeTestLaunchParser(t, []string{"env", "npm", "test"}, nil)
	if err == nil || !strings.Contains(err.Error(), "command arguments must follow --") {
		t.Fatalf("err = %v, want command-arguments delimiter error", err)
	}
}

func TestParseLaunchCommandLetsCobraRejectUnknownFlag(t *testing.T) {
	_, err := executeTestLaunchParser(t, []string{"env", "--print"}, nil)
	if err == nil || !strings.Contains(err.Error(), "unknown flag: --print") {
		t.Fatalf("err = %v, want unknown flag error", err)
	}
}

func TestConfiguredLaunchExtraArgs(t *testing.T) {
	tests := []struct {
		name          string
		args          []string
		argsLenAtDash int
		want          []string
		wantErr       bool
	}{
		{name: "none", argsLenAtDash: -1},
		{name: "after dash", args: []string{"--watch"}, argsLenAtDash: 0, want: []string{"--watch"}},
		{name: "keeps later dash", args: []string{"--", "--watch"}, argsLenAtDash: 0, want: []string{"--", "--watch"}},
		{name: "requires dash", args: []string{"--watch"}, argsLenAtDash: -1, wantErr: true},
		{name: "rejects before dash", args: []string{"env", "--watch"}, argsLenAtDash: 1, wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := configuredLaunchExtraArgs(tt.args, tt.argsLenAtDash)
			if tt.wantErr {
				if err == nil {
					t.Fatal("expected error")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("args = %#v, want %#v", got, tt.want)
			}
		})
	}
}

func executeTestLaunchParser(t *testing.T, args []string, contextTools []tool.Tool) (launchconfig.DirectLaunch, error) {
	t.Helper()
	primary := contextTool{Base: tool.Base{Metadata: tool.Metadata{Name: tool.OpenCodeToolName, LaunchHelp: "Launch OpenCode"}}}
	var parsed launchconfig.DirectLaunch
	cmd := &cobra.Command{
		Use:           primary.CommandName(),
		SilenceUsage:  true,
		SilenceErrors: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			var err error
			parsed, err = parseLaunchCommand(cmd, args, primary.Name(), contextTools)
			return err
		},
	}
	addSandboxFlags(cmd)
	cmd.PersistentFlags().Bool("debug", false, "")
	cmd.PersistentFlags().Bool("yolo", false, "")
	cmd.Flags().Bool("install", false, "")
	cmd.Flags().Bool("upgrade", false, "")
	addContextFlags(cmd, primary, contextTools)
	cmd.SetArgs(args)
	return parsed, cmd.Execute()
}
