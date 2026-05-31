package commands

import (
	"fmt"
	"os"
	"strings"

	"petris.dev/toby/internal/cli/launchconfig"
	"petris.dev/toby/internal/cli/session"
	"petris.dev/toby/internal/diagnostic/exitcode"
	"petris.dev/toby/internal/version"

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
			return session.Run(cmd.Context(), sessionParams(params), &launch.Options, launch.Extra, launch.RequestedTools, launch.Primary)
		},
	}
	cmd.SetOut(stdout)
	cmd.SetErr(stderr)
	cmd.SetArgs(params.Args)
	cmd.SetVersionTemplate("{{.Version}}\n")
	cmd.PersistentFlags().StringVar(&configPath, "config", "", "Launch from a YAML or JSON configuration file.")

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

func sessionParams(params Params) session.Params {
	return session.Params{
		Registry:       params.Registry,
		SandboxFactory: params.SandboxFactory,
		Paths:          params.Paths,
		ContextFiles:   params.ContextFiles,
		ContextInit:    params.ContextInit,
		HostManager:    params.HostManager,
		MCPServer:      params.MCPServer,
		TobyConfig:     params.TobyConfig,
		Stderr:         params.Stderr,
	}
}
