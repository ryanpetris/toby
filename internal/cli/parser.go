package cli

// Launch-invocation parsing: turns Cobra flags plus the post-`--` passthrough
// args into a resolved launchconfig.DirectLaunch.

import (
	"strings"

	appconfig "petris.dev/toby/internal/config/app"
	"petris.dev/toby/internal/config/launch"
	"petris.dev/toby/internal/diagnostic/exitcode"
	"petris.dev/toby/internal/oci/image"
	"petris.dev/toby/internal/tools"

	"github.com/spf13/cobra"
)

func parseLaunchCommand(cmd *cobra.Command, args []string, primary string, contextTools []tools.Tool) (launchconfig.DirectLaunch, error) {
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
	quiet, err := flags.GetBool("quiet")
	if err != nil {
		return result, err
	}
	result.Options.Quiet = quiet

	debug, err := flags.GetBool("debug")
	if err != nil {
		return result, err
	}
	if quiet && debug {
		return result, exitcode.New(
			2,
			"--debug and --quiet are mutually exclusive",
		)
	}

	project, err := flags.GetString("project")
	if err != nil {
		return result, err
	}
	result.Options.Project = project
	imageReference, err := flags.GetString("image")
	if err != nil {
		return result, err
	}
	result.Overrides.Image = imageReference
	if flagChanged(cmd, "pull") {
		pull, err := flags.GetString("pull")
		if err != nil {
			return result, err
		}
		switch policy := image.PullPolicy(pull); policy {
		case image.PullIfMissing, image.PullAlways, image.PullNever:
			result.Overrides.Pull = policy
		default:
			return result, exitcode.New(
				2,
				"--pull must be one of if-missing, always, or never",
			)
		}
	}
	applyDebugFlag(cmd, &result.Overrides)
	applyYoloFlag(cmd, &result.Overrides)
	applyManagedTerminalFlag(cmd, &result.Overrides)
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

func applyDebugFlag(cmd *cobra.Command, overrides *appconfig.LaunchOverrides) {
	if cmd == nil || overrides == nil || !flagChanged(cmd, "debug") {
		return
	}
	debug, err := cmd.Flags().GetBool("debug")
	if err != nil {
		return
	}
	overrides.Debug = &debug
}

func applyYoloFlag(cmd *cobra.Command, overrides *appconfig.LaunchOverrides) {
	if cmd == nil || overrides == nil || !flagChanged(cmd, "yolo") {
		return
	}
	yolo, err := cmd.Flags().GetBool("yolo")
	if err != nil {
		return
	}
	overrides.Yolo = &yolo
}

func applyManagedTerminalFlag(cmd *cobra.Command, overrides *appconfig.LaunchOverrides) {
	if cmd == nil || overrides == nil || !flagChanged(cmd, "managed-terminal") {
		return
	}
	managed, err := cmd.Flags().GetBool("managed-terminal")
	if err != nil {
		return
	}
	overrides.ManagedTerminal = &managed
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
