package cli

import (
	"context"
	"fmt"
	"io"
	"os"
	pathpkg "path"
	"strings"

	"petris.dev/toby/internal/claudeconfig"
	"petris.dev/toby/internal/control"
	"petris.dev/toby/internal/exitcode"
	"petris.dev/toby/internal/mcpserver"
	"petris.dev/toby/internal/opencodeconfig"
	"petris.dev/toby/internal/sandbox"
	"petris.dev/toby/internal/staticfiles"
	"petris.dev/toby/internal/staticmount"
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
	cmd.AddCommand(newSandboxCommand())
	cmd.AddCommand(newMCPCommand())
	cmd.AddCommand(newConfirmMountCommand())
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
			parsed, err := parseSandboxArgs(args, true, primary.Name(), contextTools, nil)
			if err != nil {
				return err
			}
			if parsed.Help {
				return cmd.Help()
			}
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
	cmd.Flags().String("project", "", "Project directory to mount and chdir into. Defaults to $XDG_PROJECTS_DIR/<env> when omitted.")
	cmd.Flags().Bool("mountable-projects", false, "Expose mountable project directories through Toby MCP and the sandbox FUSE project root.")
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
	if err := toolset.HostInit(ctx, opts); err != nil {
		return err
	}
	if err := sbx.EnsureHome(); err != nil {
		return err
	}
	if err := sbx.EnsureSandboxProjectDir(); err != nil {
		return err
	}
	homeFS, err := startOptionalHomeFS(ctx, params, sbx, toolset, opts.MountableProjects)
	if err != nil {
		return err
	}
	var tobyMount *control.Mount
	defer func() {
		_ = homeFS.Close()
		_ = tobyMount.Close()
	}()
	if homeFS != nil {
		controlManager := &control.Manager{Mounter: homeFS, Confirmer: control.TmuxConfirmer{}, ProjectRoot: sbx.ProjectRoot(), MountableProjects: opts.MountableProjects}
		tobyBase, err := sbx.TobyMountBasePath()
		if err != nil {
			return err
		}
		tobyMount, err = control.NewMountWithCurrentBinaryAt(tobyBase, controlManager.Handle, control.WithMountableProjects(opts.MountableProjects))
		if err == nil {
			err = homeFS.AddOverlayMount(tobyMount)
		}
		if err == nil {
			err = addStaticOverlayMount(ctx, params.Stderr, sbx, homeFS, tobyBase, opts.MountableProjects)
		}
		if err != nil {
			_ = tobyMount.Close()
			tobyMount = nil
			_ = homeFS.Close()
			homeFS = nil
			if opts.MountableProjects {
				return fmt.Errorf("mountable projects require FUSE-backed sandbox features: %w", err)
			}
			warnFUSEUnavailable(params.Stderr, fmt.Errorf("failed to enable FUSE-backed sandbox features: %w", err))
		}
	}
	env := tool.EnvironmentFromList(os.Environ())
	run := &tool.RunContext{
		Sandbox:     sbx,
		Options:     opts,
		Extra:       extra,
		Toolset:     toolset,
		Env:         env,
		StaticMount: homeFS != nil,
	}
	sbx.SetupContext(run)
	if err := toolset.SandboxContextSetup(run); err != nil {
		return err
	}
	if homeFS == nil && env["OPENCODE_CONFIG_DIR"] == sbx.TobyOpenCodeConfigDir() {
		delete(env, "OPENCODE_CONFIG_DIR")
	}
	commandMounts := sbx.CommandMountsWithoutFUSE(toolset)
	if homeFS != nil {
		commandMounts = homeFS.CommandMounts()
	}
	run.Exec = func(ctx context.Context, argv []string, options tool.ExecOptions) (int, error) {
		return sbx.Run(ctx, argv, commandMounts, env, options)
	}
	run.Launch = run.Exec
	if err := toolset.SandboxInit(ctx, run); err != nil {
		return err
	}
	if execMode {
		return tool.RunCommand(ctx, run.Launch, commandOrShell(extra, env), tool.ExecOptions{})
	}
	return toolset.Launch(ctx, run)
}

func startOptionalHomeFS(ctx context.Context, params Params, sbx *sandbox.Sandbox, toolset *tool.Toolset, required bool) (*sandbox.HomeFS, error) {
	if err := checkFUSEAvailable(); err != nil {
		if required {
			return nil, fmt.Errorf("mountable projects require FUSE: %w", err)
		}
		warnFUSEUnavailable(params.Stderr, err)
		return nil, nil
	}
	homeFS, err := sbx.StartHomeFS(ctx, toolset)
	if err != nil {
		if required {
			return nil, fmt.Errorf("mountable projects require FUSE: %w", err)
		}
		warnFUSEUnavailable(params.Stderr, err)
		return nil, nil
	}
	return homeFS, nil
}

func addStaticOverlayMount(ctx context.Context, stderr io.Writer, sbx *sandbox.Sandbox, homeFS *sandbox.HomeFS, tobyBase string, mountableProjects bool) error {
	staticFiles, err := buildStaticFiles(ctx, stderr, sbx, mountableProjects)
	if err != nil {
		return err
	}
	staticMount, err := staticmount.New("toby-static", pathpkg.Join(tobyBase, "static"), staticFiles)
	if err != nil {
		return err
	}
	return homeFS.AddOverlayMount(staticMount)
}

func buildStaticFiles(ctx context.Context, stderr io.Writer, sbx *sandbox.Sandbox, mountableProjects bool) ([]staticmount.File, error) {
	instructions := []string{sbx.TobyGitAgentsPath()}
	if mountableProjects {
		instructions = append(instructions, sbx.TobyProjectMountAgentsPath())
	}
	agentFiles := staticfiles.AgentFiles(mountableProjects)
	files := append([]staticmount.File(nil), agentFiles...)
	opencodeFiles, warnings, err := opencodeconfig.StaticFiles(ctx, sbx.OpenCodeConfigDir(), sbx.ProjectRoot(), instructions, opencodeconfig.WithMountableProjects(mountableProjects))
	if err != nil {
		return nil, err
	}
	for _, warning := range warnings {
		warnOpenCodeModelFetch(stderr, warning)
	}
	files = append(files, opencodeFiles...)
	instructionContent := make([][]byte, 0, len(agentFiles))
	for _, file := range agentFiles {
		instructionContent = append(instructionContent, file.Data)
	}
	claudeFiles, err := claudeconfig.StaticFiles(sbx.ProjectRoot(), instructionContent, mountableProjects)
	if err != nil {
		return nil, err
	}
	files = append(files, claudeFiles...)
	return files, nil
}

func warnOpenCodeModelFetch(stderr io.Writer, err error) {
	if stderr == nil {
		stderr = os.Stderr
	}
	_, _ = fmt.Fprintf(stderr, "toby: failed to fetch OpenCode models: %v\n", err)
}

func checkFUSEAvailable() error {
	if _, err := os.Stat("/dev/fuse"); err != nil {
		return fmt.Errorf("/dev/fuse is not available: %w", err)
	}
	return nil
}

func warnFUSEUnavailable(stderr io.Writer, err error) {
	if stderr == nil {
		stderr = os.Stderr
	}
	_, _ = fmt.Fprintf(stderr, "toby: FUSE is unavailable: %v; Toby MCP, sandbox control commands, synthetic configuration, and mountable projects will not be available.\n", err)
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

func newSandboxCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "sandbox",
		Short: "Run Toby sandbox control commands.",
	}
	cmd.AddCommand(newSandboxProjectCommand())
	cmd.AddCommand(newSandboxGitCommand())
	return cmd
}

func newSandboxProjectCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "project",
		Short: "Run project control commands inside a sandbox.",
	}
	cmd.AddCommand(&cobra.Command{
		Use:   "list",
		Short: "List available project directories.",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			client, err := sandboxControlClient()
			if err != nil {
				return err
			}
			result, err := client.ProjectList()
			if err != nil {
				return err
			}
			for _, project := range result.Projects {
				_, _ = fmt.Fprintln(cmd.OutOrStdout(), project.Name)
			}
			return nil
		},
	})
	cmd.AddCommand(&cobra.Command{
		Use:   "readme NAME",
		Short: "Read a project's README.md without mounting it.",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			client, err := sandboxControlClient()
			if err != nil {
				return err
			}
			result, err := client.ProjectReadme(args[0])
			if err != nil {
				return err
			}
			_, _ = fmt.Fprint(cmd.OutOrStdout(), result.Content)
			return nil
		},
	})
	cmd.AddCommand(&cobra.Command{
		Use:   "mount NAME",
		Short: "Request access to a project directory.",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			client, err := sandboxControlClient()
			if err != nil {
				return err
			}
			result, err := client.ProjectMount(args[0])
			if err != nil {
				return err
			}
			_, _ = fmt.Fprintln(cmd.OutOrStdout(), result.SandboxPath)
			return nil
		},
	})
	return cmd
}

func newSandboxGitCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "git",
		Short: "Run host Git commands for visible repositories.",
	}
	commit := &cobra.Command{
		Use:   "commit REPOSITORY -m MESSAGE",
		Short: "Commit staged files in a visible repository using host Git.",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			message, _ := cmd.Flags().GetString("message")
			if message == "" {
				return exitcode.New(2, "commit message is required")
			}
			client, err := sandboxControlClient()
			if err != nil {
				return err
			}
			result, err := client.GitCommit(args[0], message)
			if err != nil {
				return err
			}
			return writeGitResult(cmd, result)
		},
	}
	commit.Flags().StringP("message", "m", "", "Commit message passed to git commit -m.")
	cmd.AddCommand(commit)
	cmd.AddCommand(&cobra.Command{
		Use:   "fetch REPOSITORY",
		Short: "Fetch remote refs in a visible repository using host Git.",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			client, err := sandboxControlClient()
			if err != nil {
				return err
			}
			result, err := client.GitFetch(args[0])
			if err != nil {
				return err
			}
			return writeGitResult(cmd, result)
		},
	})
	cmd.AddCommand(&cobra.Command{
		Use:   "push REPOSITORY BRANCH [ORIGIN]",
		Short: "Push one branch from a visible repository using host Git.",
		Args:  cobra.RangeArgs(2, 3),
		RunE: func(cmd *cobra.Command, args []string) error {
			origin := ""
			if len(args) == 3 {
				origin = args[2]
			}
			client, err := sandboxControlClient()
			if err != nil {
				return err
			}
			result, err := client.GitPush(args[0], args[1], origin)
			if err != nil {
				return err
			}
			return writeGitResult(cmd, result)
		},
	})
	return cmd
}

func sandboxControlClient() (*control.Client, error) {
	path, err := control.DefaultControlPath()
	if err != nil {
		return nil, err
	}
	if _, err := os.Stat(path); err != nil {
		return nil, fmt.Errorf("toby sandbox commands must run inside a Toby sandbox: %s is not available", path)
	}
	return control.NewClient(path), nil
}

func writeGitResult(cmd *cobra.Command, result control.GitResult) error {
	if result.Stdout != "" {
		_, _ = fmt.Fprint(cmd.OutOrStdout(), result.Stdout)
	}
	if result.Stderr != "" {
		_, _ = fmt.Fprint(cmd.ErrOrStderr(), result.Stderr)
	}
	if result.ExitCode != 0 {
		return exitcode.Code(result.ExitCode)
	}
	return nil
}

func newMCPCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "mcp",
		Short: "Run the Toby MCP server inside a sandbox.",
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(args) != 0 {
				return exitcode.New(2, "mcp does not accept arguments")
			}
			return mcpserver.Run(cmd.Context(), "")
		},
	}
}

func newConfirmMountCommand() *cobra.Command {
	return &cobra.Command{
		Use:    "__confirm-mount PATH",
		Hidden: true,
		Args:   cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			approved, err := control.RunMountConfirmation(cmd.Context(), args[0])
			if err != nil {
				return err
			}
			if !approved {
				return exitcode.Code(1)
			}
			return nil
		},
	}
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
