// Package cli builds Toby's Cobra command tree: the root command, the per-tool
// launch commands, agent and resource subcommands, and shell completion, wired
// from the injected Params. NewRootCommand assembles it; ExecuteAndReport runs it.
package cli

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"

	appconfig "petris.dev/toby/internal/config/app"
	"petris.dev/toby/internal/config/launch"
	"petris.dev/toby/internal/diagnostic"
	"petris.dev/toby/internal/diagnostic/exitcode"
	"petris.dev/toby/internal/tools"
	"petris.dev/toby/internal/version"

	"github.com/spf13/cobra"
)

const (
	rootCommandGroupAI      = "ai-tools"
	rootCommandGroupTools   = "other-tools"
	rootCommandGroupStorage = "storage"
	rootCommandGroupSystem  = "system"
)

// NewRootCommand constructs Toby's root CLI command.
func NewRootCommand(params Params) *cobra.Command {
	stdin := params.Stdin
	if stdin == nil {
		stdin = os.Stdin
	}
	stdout := params.Stdout
	if stdout == nil {
		stdout = os.Stdout
	}
	stderr := params.Stderr
	if stderr == nil {
		stderr = os.Stderr
	}
	flags := rootFlagValues{
		managedTerminal: true,
	}
	cmd := &cobra.Command{
		Use:              "toby",
		Short:            "Run Toby Sandbox development environments.",
		Long:             "Toby Sandbox runs development tools inside private-home development sandboxes.",
		Version:          version.String(),
		SilenceUsage:     true,
		SilenceErrors:    true,
		TraverseChildren: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			if flagChanged(cmd, "config") &&
				strings.TrimSpace(flags.configPath) == "" {
				return exitcode.New(2, "--config requires a value")
			}
			if flags.configPath == "" {
				return cmd.Help()
			}
			if flags.debug && flags.quiet {
				return exitcode.New(
					2,
					"--debug and --quiet are mutually exclusive",
				)
			}
			extra, err := configuredLaunchExtraArgs(args, cmd.Flags().ArgsLenAtDash())
			if err != nil {
				return err
			}
			launch, err := launchconfig.BuildConfiguredLaunch(
				launchConfigParams(params),
				flags.configPath,
				extra,
			)
			if err != nil {
				return err
			}
			applyDebugFlag(cmd, &launch.Overrides)
			applyYoloFlag(cmd, &launch.Overrides)
			applyManagedTerminalFlag(cmd, &launch.Overrides)
			launch.Options.Quiet = flags.quiet
			return runSession(cmd.Context(), params, &launch.Options, launch.Overrides, launch.Extra, launch.RequestedTools, launch.Primary)
		},
	}
	cmd.SetIn(stdin)
	cmd.SetOut(stdout)
	cmd.SetErr(stderr)
	cmd.SetArgs(params.Args)
	cmd.SetVersionTemplate("{{.Version}}\n")
	addRootPersistentFlags(cmd, &flags)

	cmd.AddGroup(
		&cobra.Group{ID: rootCommandGroupAI, Title: "AI coding tools:"},
		&cobra.Group{ID: rootCommandGroupTools, Title: "Other tools:"},
		&cobra.Group{ID: rootCommandGroupStorage, Title: "Storage commands:"},
		&cobra.Group{ID: rootCommandGroupSystem, Title: "System commands:"},
	)
	cmd.SetHelpCommandGroupID(rootCommandGroupSystem)

	storageCommands := []*cobra.Command{
		newVolumeCommand(params),
		newImageCommand(params, &flags),
	}
	for _, command := range storageCommands {
		command.GroupID = rootCommandGroupStorage
		cmd.AddCommand(command)
	}

	systemCommands := []*cobra.Command{
		newAgentCommand(params),
		newCompletionCommand(),
	}
	for _, command := range systemCommands {
		command.GroupID = rootCommandGroupSystem
		cmd.AddCommand(command)
	}

	for _, item := range params.Registry.LaunchTools() {
		command := newLaunchCommand(params, item, &flags.configPath)
		command.GroupID = rootCommandGroupTools
		if item.Group() == tools.GroupAI {
			command.GroupID = rootCommandGroupAI
		}
		cmd.AddCommand(command)
	}
	return cmd
}

type rootFlagValues struct {
	configPath      string
	debug           bool
	quiet           bool
	yolo            bool
	managedTerminal bool
}

func addRootPersistentFlags(
	cmd *cobra.Command,
	values *rootFlagValues,
) {
	cmd.PersistentFlags().StringVar(
		&values.configPath,
		"config",
		"",
		"Launch from a YAML or JSON configuration file.",
	)
	cmd.PersistentFlags().BoolVar(
		&values.debug,
		"debug",
		false,
		"Enable Toby debug mode for this launch.",
	)
	cmd.PersistentFlags().BoolVar(
		&values.quiet,
		"quiet",
		false,
		"Suppress non-foreground launch output.",
	)
	cmd.PersistentFlags().BoolVar(
		&values.yolo,
		"yolo",
		false,
		"Launch the tool with its permission-bypass flag for this launch.",
	)
	cmd.PersistentFlags().BoolVar(
		&values.managedTerminal,
		"managed-terminal",
		true,
		"Run the foreground tool under Toby's managed terminal (approval prompts); --managed-terminal=false uses a plain passthrough.",
	)
}

// ExecuteAndReport executes cmd, reports its error, and returns an exit code.
func ExecuteAndReport(ctx context.Context, cmd *cobra.Command) int {
	err := cmd.ExecuteContext(ctx)
	if err == nil {
		return 0
	}

	// Requested shutdown stays quiet after command teardown. A signal-aware
	// cancellation cause preserves its shell exit code; plain administrative
	// cancellation remains successful.
	if ctx.Err() != nil && errors.Is(err, context.Canceled) {
		cause := context.Cause(ctx)
		if cause == nil || cause == context.Canceled {
			return 0
		}
		return exitcode.FromError(cause)
	}

	if !exitcode.IsSilent(err) {
		_, writeErr := fmt.Fprintln(cmd.ErrOrStderr(), err)
		diagnostic.DiscardError(
			"the command error is already determined",
			"write command error",
			writeErr,
		)
	}
	return exitcode.FromError(err)
}

func launchConfigParams(params Params) launchconfig.Params {
	return launchconfig.Params{
		Registry: params.Registry,
		Paths:    params.Paths,
		Config:   params.TobyConfig,
		Warnings: params.Warnings,
		Logger:   params.Diagnostics.Logger("config.launch"),
	}
}

func runSession(ctx context.Context, params Params, opts *tools.Options, overrides appconfig.LaunchOverrides, extra, requestedTools []string, primary string) error {
	if params.SessionRunner == nil {
		return fmt.Errorf("session runner is not configured")
	}
	return params.SessionRunner.Run(ctx, opts, overrides, extra, requestedTools, primary)
}
