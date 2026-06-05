// Package cli builds Toby's Cobra command tree: the root command, the per-tool
// launch commands, the `toby sandbox` subcommands, and shell completion, wired
// from the injected Params. NewRootCommand assembles it; ExecuteAndReport runs it.
package cli

import (
	"context"
	"fmt"
	"os"
	"strings"

	"petris.dev/toby/config/launch"
	"petris.dev/toby/diagnostic/exitcode"
	"petris.dev/toby/tools"
	"petris.dev/toby/version"

	"github.com/spf13/cobra"
)

func NewRootCommand(params Params) *cobra.Command {
	stdout := params.Stdout
	if stdout == nil {
		stdout = os.Stdout
	}
	stderr := params.Stderr
	if stderr == nil {
		stderr = os.Stderr
	}
	var configPath string
	var debug bool
	var yolo bool
	cmd := &cobra.Command{
		Use:              "toby",
		Short:            "Run Toby Sandbox development environments.",
		Long:             "Toby Sandbox runs development tools inside private-home development sandboxes.",
		Version:          version.String(),
		SilenceUsage:     true,
		SilenceErrors:    true,
		TraverseChildren: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			if flagChanged(cmd, "config") && strings.TrimSpace(configPath) == "" {
				return exitcode.New(2, "--config requires a value")
			}
			if configPath == "" {
				return cmd.Help()
			}
			extra, err := configuredLaunchExtraArgs(args, cmd.Flags().ArgsLenAtDash())
			if err != nil {
				return err
			}
			launch, err := launchconfig.BuildConfiguredLaunch(launchConfigParams(params), configPath, extra)
			if err != nil {
				return err
			}
			applyDebugFlag(cmd, &launch.Options)
			applyYoloFlag(cmd, &launch.Options)
			return runSession(cmd.Context(), params, &launch.Options, launch.Extra, launch.RequestedTools, launch.Primary)
		},
	}
	cmd.SetOut(stdout)
	cmd.SetErr(stderr)
	cmd.SetArgs(params.Args)
	cmd.SetVersionTemplate("{{.Version}}\n")
	cmd.PersistentFlags().StringVar(&configPath, "config", "", "Launch from a YAML or JSON configuration file.")
	cmd.PersistentFlags().BoolVar(&debug, "debug", false, "Enable Toby debug mode for this launch.")
	cmd.PersistentFlags().BoolVar(&yolo, "yolo", false, "Launch the tool with its permission-bypass flag for this launch.")

	cmd.AddCommand(newSandboxCommand(params))
	for _, item := range params.Registry.LaunchTools() {
		cmd.AddCommand(newLaunchCommand(params, item, &configPath))
	}
	cmd.AddCommand(newCompletionCommand())
	return cmd
}

func ExecuteAndReport(cmd *cobra.Command) int {
	err := cmd.Execute()
	if err != nil && err.Error() != "" {
		fmt.Fprintln(cmd.ErrOrStderr(), err)
	}
	return exitcode.FromError(err)
}

func launchConfigParams(params Params) launchconfig.Params {
	return launchconfig.Params{
		Registry: params.Registry,
		Paths:    params.Paths,
		Config:   params.TobyConfig,
		Stderr:   params.Stderr,
	}
}

func runSession(ctx context.Context, params Params, opts *tools.Options, extra, requestedTools []string, primary string) error {
	if params.SessionRunner == nil {
		return fmt.Errorf("session runner is not configured")
	}
	return params.SessionRunner.Run(ctx, opts, extra, requestedTools, primary)
}
