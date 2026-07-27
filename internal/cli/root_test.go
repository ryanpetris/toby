package cli

// Tests root command construction, grouping, and output behavior.

import (
	"bytes"
	"strings"
	"testing"

	"petris.dev/toby/internal/tools"
	"petris.dev/toby/internal/version"

	"github.com/spf13/cobra"
)

func TestExecCommandGeneratedFromRegisteredTool(t *testing.T) {
	registry, err := tools.NewRegistry([]tools.Tool{
		configTestTool{Base: tools.Base{Metadata: tools.Metadata{Name: "exec", Group: tools.GroupCommand, LaunchHelp: "Run a command"}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	cmd := NewRootCommand(Params{Registry: registry})
	if findCommand(cmd, "exec") == nil {
		t.Fatal("exec command missing")
	}
}

func TestRootHelpGroupsCommands(t *testing.T) {
	registry, err := tools.NewRegistry([]tools.Tool{
		configTestTool{Base: tools.Base{Metadata: tools.Metadata{
			Name:       "codex",
			Group:      tools.GroupAI,
			LaunchHelp: "Launch Codex",
		}}},
		configTestTool{Base: tools.Base{Metadata: tools.Metadata{
			Name:       "exec",
			Group:      tools.GroupCommand,
			LaunchHelp: "Run a command",
		}}},
	})
	if err != nil {
		t.Fatal(err)
	}

	var stdout bytes.Buffer
	cmd := NewRootCommand(Params{
		Registry: registry,
		Args:     []string{"--help"},
		Stdout:   &stdout,
	})
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}

	help := stdout.String()
	ai := strings.Index(help, "AI coding tools:")
	other := strings.Index(help, "Other tools:")
	storage := strings.Index(help, "Storage commands:")
	system := strings.Index(help, "System commands:")
	if ai < 0 || other < 0 || storage < 0 || system < 0 {
		t.Fatalf("help is missing command groups:\n%s", help)
	}
	if !(ai < other && other < storage && storage < system) {
		t.Fatalf("command groups are out of order:\n%s", help)
	}

	for name, want := range map[string]string{
		"codex":      rootCommandGroupAI,
		"exec":       rootCommandGroupTools,
		"agent":      rootCommandGroupSystem,
		"volume":     rootCommandGroupStorage,
		"image":      rootCommandGroupStorage,
		"completion": rootCommandGroupSystem,
		"help":       rootCommandGroupSystem,
	} {
		command := findCommand(cmd, name)
		if command == nil {
			t.Fatalf("%s command missing", name)
		}
		if command.GroupID != want {
			t.Errorf("%s group = %q, want %q", name, command.GroupID, want)
		}
	}
}

func TestAgentCommandIncludesResourceOperations(t *testing.T) {
	root := NewRootCommand(Params{Registry: emptyRegistry(t)})
	agent := findCommand(root, agentCommandName)
	if agent == nil {
		t.Fatal("agent command missing")
	}
	for _, name := range []string{
		agentStatusCommandName,
		agentStopCommandName,
		agentResourcesCommandName,
		agentLogsCommandName,
		agentModelsCommandName,
		agentCacheCommandName,
	} {
		if findCommand(agent, name) == nil {
			t.Fatalf("agent %s command missing", name)
		}
	}
	cache := findCommand(agent, agentCacheCommandName)
	if findCommand(cache, agentCacheFlushCommandName) == nil {
		t.Fatal("agent cache flush command missing")
	}
}

func TestVolumeCommandIncludesManagementOperations(t *testing.T) {
	root := NewRootCommand(Params{Registry: emptyRegistry(t)})
	volume := findCommand(root, volumeCommandName)
	if volume == nil {
		t.Fatal("volume command missing")
	}
	for _, name := range []string{
		volumeCreateCommandName,
		volumeListCommandName,
		volumeInspectCommandName,
		volumePathCommandName,
		volumeRemoveCommandName,
	} {
		if findCommand(volume, name) == nil {
			t.Fatalf("volume %s command missing", name)
		}
	}
	assertCommandAliases(
		t,
		findCommand(volume, volumeListCommandName),
		managementListAlias,
	)
	assertCommandAliases(
		t,
		findCommand(volume, volumeRemoveCommandName),
		managementRemoveAlias,
	)
}

func TestImageCommandIncludesManagementOperations(t *testing.T) {
	root := NewRootCommand(Params{Registry: emptyRegistry(t)})
	image := findCommand(root, imageCommandName)
	if image == nil {
		t.Fatal("image command missing")
	}
	for _, name := range []string{
		imagePullCommandName,
		imageBuildCommandName,
		imageImportCommandName,
		imageListCommandName,
		imageInspectCommandName,
		imagePathCommandName,
		imageRemoveCommandName,
		imagePruneCommandName,
	} {
		if findCommand(image, name) == nil {
			t.Fatalf("image %s command missing", name)
		}
	}
	assertCommandAliases(
		t,
		findCommand(image, imageListCommandName),
		managementListAlias,
	)
	assertCommandAliases(
		t,
		findCommand(image, imageRemoveCommandName),
		managementRemoveAlias,
	)
}

func TestRootConfigFlagRejectsEmptyValue(t *testing.T) {
	cmd := NewRootCommand(Params{Registry: emptyRegistry(t), Args: []string{"--config", ""}})
	err := cmd.Execute()
	if err == nil || !strings.Contains(err.Error(), "--config requires a value") {
		t.Fatalf("err = %v, want config value error", err)
	}
}

func TestLaunchConfigFlagRejectsEmptyValue(t *testing.T) {
	registry, err := tools.NewRegistry([]tools.Tool{
		configTestTool{Base: tools.Base{Metadata: tools.Metadata{Name: "opencode", LaunchHelp: "Launch OpenCode"}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	cmd := NewRootCommand(Params{Registry: registry, Args: []string{"opencode", "env", "--config", ""}})
	err = cmd.Execute()
	if err == nil || !strings.Contains(err.Error(), "--config requires a value") {
		t.Fatalf("err = %v, want config value error", err)
	}
}

func TestVersionFlagPrintsVersion(t *testing.T) {
	old := version.Current
	version.Current = "v1.2.3"
	t.Cleanup(func() { version.Current = old })

	var stdout bytes.Buffer
	cmd := NewRootCommand(Params{Registry: emptyRegistry(t), Args: []string{"--version"}, Stdout: &stdout})
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	if got := stdout.String(); got != "v1.2.3\n" {
		t.Fatalf("version output = %q, want %q", got, "v1.2.3\n")
	}
}

func TestVersionFlagDefaultsToDev(t *testing.T) {
	old := version.Current
	version.Current = ""
	t.Cleanup(func() { version.Current = old })

	var stdout bytes.Buffer
	cmd := NewRootCommand(Params{Registry: emptyRegistry(t), Args: []string{"--version"}, Stdout: &stdout})
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	if got := stdout.String(); got != "dev\n" {
		t.Fatalf("version output = %q, want %q", got, "dev\n")
	}
}

func emptyRegistry(t *testing.T) *tools.Registry {
	t.Helper()
	registry, err := tools.NewRegistry(nil)
	if err != nil {
		t.Fatal(err)
	}
	return registry
}

type configTestTool struct{ tools.Base }

func findCommand(cmd *cobra.Command, name string) *cobra.Command {
	for _, child := range cmd.Commands() {
		if child.Name() == name {
			return child
		}
	}
	return nil
}

func assertCommandAliases(
	t *testing.T,
	command *cobra.Command,
	want ...string,
) {
	t.Helper()
	if command == nil {
		t.Fatal("command is nil")
	}
	if strings.Join(command.Aliases, "\x00") != strings.Join(want, "\x00") {
		t.Fatalf(
			"%s aliases = %v, want %v",
			command.Name(),
			command.Aliases,
			want,
		)
	}
}
