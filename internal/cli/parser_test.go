package cli

import (
	"reflect"
	"strings"
	"testing"

	"petris.dev/toby/internal/config/launch"
	"petris.dev/toby/tools"

	"github.com/spf13/cobra"
)

type contextTool struct{ tools.Base }

func TestParseLaunchCommandUsesCobraFlagsAndPassthrough(t *testing.T) {
	ctxTools := []tools.Tool{
		contextTool{Base: tools.Base{Metadata: tools.Metadata{Name: "npm", LaunchHelp: "Launch Node Package Manager"}}},
		contextTool{Base: tools.Base{Metadata: tools.Metadata{Name: "github_cli", CLIName: "gh", LaunchHelp: "Launch GitHub CLI"}}},
	}
	parsed, err := executeTestLaunchParser(t, []string{"proj", "--debug", "--yolo", "--with-gh", "--upgrade", "--image=node:test", "--", "foo", "--", "bar"}, ctxTools)
	if err != nil {
		t.Fatal(err)
	}
	if parsed.Options.Env != "proj" || !parsed.Options.Upgrade || parsed.Overrides.Image != "node:test" {
		t.Fatalf("parsed options = %#v overrides = %#v", parsed.Options, parsed.Overrides)
	}
	if parsed.Overrides.Debug == nil || !*parsed.Overrides.Debug {
		t.Fatalf("debug = %#v", parsed.Overrides.Debug)
	}
	if parsed.Overrides.Yolo == nil || !*parsed.Overrides.Yolo {
		t.Fatalf("yolo = %#v", parsed.Overrides.Yolo)
	}
	if got, want := parsed.RequestedTools, []string{"github_cli", "opencode"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("requested = %#v, want %#v", got, want)
	}
	if got, want := parsed.Extra, []string{"foo", "--", "bar"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("extra = %#v, want %#v", got, want)
	}
}

func TestParseLaunchCommandCollectsPublishFlags(t *testing.T) {
	parsed, err := executeTestLaunchParser(t, []string{"proj", "-p", "8080:3000", "--publish", "127.0.0.1:9090:9090"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := parsed.Overrides.Ports, []string{"8080:3000", "127.0.0.1:9090:9090"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("ports = %#v, want %#v", got, want)
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

func executeTestLaunchParser(t *testing.T, args []string, contextTools []tools.Tool) (launchconfig.DirectLaunch, error) {
	t.Helper()
	primary := contextTool{Base: tools.Base{Metadata: tools.Metadata{Name: "opencode", LaunchHelp: "Launch OpenCode"}}}
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
