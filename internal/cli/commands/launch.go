package commands

import (
	"strings"

	"petris.dev/toby/internal/cli/launchconfig"
	"petris.dev/toby/internal/cli/session"
	"petris.dev/toby/internal/diagnostic/exitcode"
	"petris.dev/toby/internal/tools/tool"

	"github.com/spf13/cobra"
)

func configuredLaunchExtraArgs(args []string, argsLenAtDash int) ([]string, error) {
	if len(args) == 0 {
		return nil, nil
	}
	if argsLenAtDash < 0 {
		return nil, unexpectedLaunchArgument(args[0])
	}
	if argsLenAtDash > 0 {
		return nil, unexpectedLaunchArgument(args[0])
	}
	return args, nil
}

func newLaunchCommand(params Params, primary tool.Tool, rootConfigPath *string) *cobra.Command {
	contextNames := tool.ExpandGroups(primary.ContextGroups())
	contextTools := toolsFromNames(params.Registry, contextNames)
	cmd := &cobra.Command{
		Use:   primary.CommandName() + " [env] [-- command arguments...]",
		Short: primary.LaunchHelp(),
		RunE: func(cmd *cobra.Command, args []string) error {
			effectiveConfigPath := ""
			if rootConfigPath != nil {
				effectiveConfigPath = *rootConfigPath
			}
			if flagChanged(cmd, "config") && strings.TrimSpace(effectiveConfigPath) == "" {
				return exitcode.New(2, "--config requires a value")
			}
			parsed, err := parseLaunchCommand(cmd, args, primary.Name(), contextTools)
			if err != nil {
				return err
			}
			if effectiveConfigPath != "" {
				project, err := launchconfig.ResolveDirectLaunchProject(params.Paths, parsed.Options)
				if err != nil {
					return err
				}
				launch, err := launchconfig.BuildOverlayConfiguredLaunch(launchConfigParams(params), effectiveConfigPath, parsed, primary.Name(), project)
				if err != nil {
					return err
				}
				return session.Run(cmd.Context(), sessionParams(params), &launch.Options, launch.Extra, launch.RequestedTools, launch.Primary)
			}
			launch, ok, err := launchconfig.MaybeAutoloadProjectConfig(launchConfigParams(params), parsed, primary.Name())
			if err != nil {
				return err
			}
			if ok {
				return session.Run(cmd.Context(), sessionParams(params), &launch.Options, launch.Extra, launch.RequestedTools, launch.Primary)
			}
			return session.Run(cmd.Context(), sessionParams(params), &parsed.Options, parsed.Extra, parsed.RequestedTools, primary.Name())
		},
	}
	addSandboxFlags(cmd)
	cmd.Flags().Bool("install", false, "Install "+primary.CommandName()+" inside the sandbox instead of launching it.")
	cmd.Flags().Bool("upgrade", false, "Reinstall "+primary.CommandName()+" inside the sandbox, then launch it.")
	primary.ConfigureCommand(cmd)
	addContextFlags(cmd, primary, contextTools)
	return cmd
}

func addSandboxFlags(cmd *cobra.Command) {
	cmd.Flags().String("project", "", "Project directory to mount and chdir into. Defaults to $XDG_PROJECTS_DIR/<env> when omitted.")
	cmd.Flags().String("sandbox-runtime", "", "Sandbox runtime to use: bubblewrap or docker.")
	cmd.Flags().String("sandbox-image", "", "Docker image to use when --sandbox-runtime=docker.")
	cmd.Flags().String("tool-state", "", "Tool state source to use by default: private or host.")
	cmd.Flags().String("tool-state-root", "", "Host root to use as HOME for tool state when --tool-state=host.")
}

func addContextFlags(cmd *cobra.Command, primary tool.Tool, contextTools []tool.Tool) {
	primaryName := ""
	if primary != nil {
		primaryName = primary.Name()
	}
	for _, item := range contextTools {
		if item.Name() == primaryName {
			continue
		}
		display := strings.TrimSpace(strings.TrimPrefix(item.LaunchHelp(), "Launch "))
		if display == "" {
			display = item.Name()
		}
		cmd.Flags().Bool("with-"+item.CommandName(), false, "Enable "+display)
	}
}

func toolsFromNames(registry *tool.Registry, names []string) []tool.Tool {
	result := make([]tool.Tool, 0, len(names))
	for _, name := range names {
		if item, ok := registry.Get(name); ok {
			result = append(result, item)
		}
	}
	return result
}
