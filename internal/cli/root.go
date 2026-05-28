package cli

import (
	"context"
	"fmt"
	"io"
	"os"
	"strings"

	"petris.dev/toby/internal/exitcode"
	"petris.dev/toby/internal/sandbox"
	"petris.dev/toby/internal/tool"

	"github.com/spf13/cobra"
)

type Params struct {
	Registry       *tool.Registry
	SandboxFactory sandbox.Factory
	Args           []string
	Stdout         io.Writer
	Stderr         io.Writer
}

func NewRootCommand(params Params) *cobra.Command {
	stdout := params.Stdout
	if stdout == nil {
		stdout = os.Stdout
	}
	stderr := params.Stderr
	if stderr == nil {
		stderr = os.Stderr
	}
	cmd := &cobra.Command{
		Use:           "toby",
		Short:         "Run Toby Sandbox development environments.",
		Long:          "Toby Sandbox runs development tools inside private-home bubblewrap sandboxes.",
		SilenceUsage:  true,
		SilenceErrors: true,
	}
	cmd.SetOut(stdout)
	cmd.SetErr(stderr)
	cmd.SetArgs(params.Args)

	for _, item := range params.Registry.LaunchTools() {
		cmd.AddCommand(newLaunchCommand(params, item))
	}
	cmd.AddCommand(newExecCommand(params))
	cmd.AddCommand(newCompletionCommand())
	return cmd
}

func newLaunchCommand(params Params, primary tool.Tool) *cobra.Command {
	contextNames := tool.ExpandGroups(primary.ContextGroups())
	contextTools := toolsFromNames(params.Registry, contextNames)
	cmd := &cobra.Command{
		Use:                primary.CommandName() + " [env] [-- command arguments...]",
		Short:              primary.LaunchHelp(),
		DisableFlagParsing: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			options := &tool.CommandOptions{}
			var parser func(string, string) (bool, string, error)
			if primary.Name() == tool.OpenCodeToolName {
				parser = opencodeArgParser(options)
			}
			parsed, err := parseSandboxArgs(args, true, primary.Name(), contextTools, parser)
			if err != nil {
				return err
			}
			if parsed.Help {
				return cmd.Help()
			}
			parsed.Options.SyncModels = options.SyncModels
			return runSandboxCommand(cmd.Context(), params, &parsed.Options, parsed.Extra, parsed.RequestedTools, primary.Name(), false)
		},
	}
	addSandboxFlags(cmd)
	cmd.Flags().Bool("install", false, "Install "+primary.CommandName()+" inside the sandbox instead of launching it.")
	cmd.Flags().Bool("upgrade", false, "Reinstall "+primary.CommandName()+" inside the sandbox, then launch it.")
	primary.ConfigureCommand(cmd)
	addContextFlags(cmd, primary, contextTools)
	return cmd
}

func newExecCommand(params Params) *cobra.Command {
	contextTools := toolsFromNames(params.Registry, tool.ExpandGroups([]string{tool.GroupAI, tool.GroupSystem, tool.GroupUI, tool.GroupVCS}))
	cmd := &cobra.Command{
		Use:                "exec [env] [-- command arguments...]",
		Short:              "Run a command in Toby Sandbox (default: interactive shell).",
		DisableFlagParsing: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			parsed, err := parseSandboxArgs(args, false, "", contextTools, nil)
			if err != nil {
				return err
			}
			if parsed.Help {
				return cmd.Help()
			}
			return runSandboxCommand(cmd.Context(), params, &parsed.Options, parsed.Extra, parsed.RequestedTools, "", true)
		},
	}
	addSandboxFlags(cmd)
	addContextFlags(cmd, nil, contextTools)
	return cmd
}

func addSandboxFlags(cmd *cobra.Command) {
	cmd.Flags().Bool("tmp-env", false, "Use a temporary sandbox home directory that is removed on exit.")
	cmd.Flags().String("project", "", "Project directory to bind-mount and chdir into. Defaults to $XDG_PROJECTS_DIR/<env> when omitted.")
	cmd.Flags().Bool("no-project", false, "Do not bind-mount a host project directory; still start in $XDG_PROJECTS_DIR/<name> inside the sandbox.")
	cmd.Flags().Bool("print", false, "Print the final sandbox command instead of executing it.")
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

func runSandboxCommand(ctx context.Context, params Params, opts *tool.CommandOptions, extra, requestedTools []string, primary string, execMode bool) error {
	sbx, err := params.SandboxFactory.FromOptions(opts)
	if err != nil {
		return err
	}
	defer sbx.Cleanup()

	toolset, err := params.Registry.Build(requestedTools, primary)
	if err != nil {
		return err
	}
	if !toolset.Has(tool.PrintToolName) {
		if err := toolset.HostInit(ctx, opts); err != nil {
			return err
		}
	}
	if err := sbx.EnsureHome(); err != nil {
		return err
	}
	if err := sbx.EnsureSandboxProjectDir(); err != nil {
		return err
	}
	env := tool.EnvironmentFromList(os.Environ())
	run := &tool.RunContext{
		Sandbox: sbx,
		Options: opts,
		Extra:   extra,
		Toolset: toolset,
		Env:     env,
	}
	run.Exec = func(ctx context.Context, argv []string, options tool.ExecOptions) (int, error) {
		return sbx.Run(ctx, argv, toolset, env, options)
	}
	run.Launch = run.Exec
	sbx.SetupContext(run)
	if err := toolset.SandboxContextSetup(run); err != nil {
		return err
	}
	if err := toolset.SandboxInit(ctx, run); err != nil {
		return err
	}
	if execMode {
		return tool.RunCommand(ctx, run.Launch, commandOrShell(extra, env), tool.ExecOptions{})
	}
	return toolset.Launch(ctx, run)
}

func commandOrShell(extra []string, env tool.Environment) []string {
	if len(extra) > 0 {
		return extra
	}
	shell := env["SHELL"]
	if shell == "" {
		shell = "/bin/sh"
	}
	return []string{shell, "-i"}
}

func newCompletionCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:                   "completion [bash|zsh|fish|powershell]",
		Short:                 "Generate shell completion script for Toby.",
		DisableFlagsInUseLine: true,
		ValidArgs:             []string{"bash", "zsh", "fish", "powershell"},
		Args:                  cobra.MatchAll(cobra.ExactArgs(1), cobra.OnlyValidArgs),
		RunE: func(cmd *cobra.Command, args []string) error {
			switch args[0] {
			case "bash":
				return cmd.Root().GenBashCompletionV2(cmd.OutOrStdout(), true)
			case "zsh":
				return cmd.Root().GenZshCompletion(cmd.OutOrStdout())
			case "fish":
				return cmd.Root().GenFishCompletion(cmd.OutOrStdout(), true)
			case "powershell":
				return cmd.Root().GenPowerShellCompletionWithDesc(cmd.OutOrStdout())
			default:
				return exitcode.New(2, "unsupported shell: %s", args[0])
			}
		},
	}
	return cmd
}

func ExecuteAndReport(cmd *cobra.Command) int {
	err := cmd.Execute()
	if err != nil && err.Error() != "" {
		fmt.Fprintln(cmd.ErrOrStderr(), err)
	}
	return exitcode.FromError(err)
}
