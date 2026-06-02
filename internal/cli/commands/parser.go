package commands

import (
	"strings"

	"petris.dev/toby/internal/cli/launchconfig"
	"petris.dev/toby/internal/diagnostic/exitcode"
	"petris.dev/toby/internal/tools/tool"

	"github.com/spf13/cobra"
)

func parseLaunchCommand(cmd *cobra.Command, args []string, primary string, contextTools []tool.Tool) (launchconfig.DirectLaunch, error) {
	var result launchconfig.DirectLaunch
	env, extra, err := launchCommandArgs(args, cmd.Flags().ArgsLenAtDash())
	if err != nil {
		return result, err
	}
	result.Options.Env = env
	result.Extra = extra

	flags := cmd.Flags()
	install, err := flags.GetBool("install")
	if err != nil {
		return result, err
	}
	upgrade, err := flags.GetBool("upgrade")
	if err != nil {
		return result, err
	}
	if install && upgrade {
		return result, exitcode.New(2, "--install and --upgrade are mutually exclusive")
	}
	result.Options.Install = install
	result.Options.Upgrade = upgrade

	project, err := flags.GetString("project")
	if err != nil {
		return result, err
	}
	result.Options.Project = project
	sandboxRuntime, err := flags.GetString("sandbox-runtime")
	if err != nil {
		return result, err
	}
	result.Options.SandboxRuntime = sandboxRuntime
	dockerImage, err := flags.GetString("sandbox-image")
	if err != nil {
		return result, err
	}
	result.Options.DockerImage = dockerImage
	for _, item := range contextTools {
		if item.Name() == primary {
			continue
		}
		selected, err := flags.GetBool("with-" + item.CommandName())
		if err != nil {
			return result, err
		}
		if selected {
			result.RequestedTools = appendIfMissing(result.RequestedTools, item.Name())
		}
	}
	if primary != "" {
		result.RequestedTools = appendIfMissing(result.RequestedTools, primary)
	}
	return result, nil
}

func launchCommandArgs(args []string, argsLenAtDash int) (string, []string, error) {
	preLen := len(args)
	if argsLenAtDash >= 0 {
		preLen = argsLenAtDash
	}
	if preLen > 1 {
		return "", nil, unexpectedLaunchArgument(args[1])
	}
	var env string
	if preLen == 1 {
		env = args[0]
	}
	if argsLenAtDash < 0 {
		return env, nil, nil
	}
	return env, append([]string(nil), args[preLen:]...), nil
}

func flagChanged(cmd *cobra.Command, name string) bool {
	flag := cmd.Flags().Lookup(name)
	return flag != nil && flag.Changed
}

func unexpectedLaunchArgument(arg string) error {
	if strings.HasPrefix(arg, "-") {
		return exitcode.New(2, "unknown argument %q; command arguments must follow --", arg)
	}
	return exitcode.New(2, "unexpected argument %q; command arguments must follow --", arg)
}

func appendIfMissing(values []string, value string) []string {
	for _, item := range values {
		if item == value {
			return values
		}
	}
	return append(values, value)
}
