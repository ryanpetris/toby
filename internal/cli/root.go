package cli

import (
	"context"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"

	"petris.dev/toby/internal/contextfiles"
	"petris.dev/toby/internal/control"
	"petris.dev/toby/internal/exitcode"
	"petris.dev/toby/internal/mcpserver"
	"petris.dev/toby/internal/sandbox"
	"petris.dev/toby/internal/tobyconfig"
	"petris.dev/toby/internal/tool"

	"github.com/spf13/cobra"
)

type Params struct {
	Registry       *tool.Registry
	SandboxFactory sandbox.Factory
	ContextFiles   *contextfiles.Service
	TobyConfig     *tobyconfig.Service
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

func NewSandboxRootCommand(params Params) *cobra.Command {
	stdout := params.Stdout
	if stdout == nil {
		stdout = os.Stdout
	}
	stderr := params.Stderr
	if stderr == nil {
		stderr = os.Stderr
	}
	cmd := &cobra.Command{
		Use:           "toby-sandbox",
		Short:         "Run Toby sandbox internals.",
		SilenceUsage:  true,
		SilenceErrors: true,
	}
	cmd.SetOut(stdout)
	cmd.SetErr(stderr)
	cmd.SetArgs(params.Args)
	cmd.AddCommand(newSandboxInitCommand())
	cmd.AddCommand(newSandboxMCPCommand())
	cmd.AddCommand(newSandboxGitCommand())
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

	env := tool.EnvironmentFromList(os.Environ())
	run := &tool.RunContext{
		Sandbox: sbx,
		Options: opts,
		Extra:   extra,
		Toolset: toolset,
		Env:     env,
		Stderr:  params.Stderr,
	}
	sbx.SetupContext(run)
	if err := toolset.SandboxContextSetup(run); err != nil {
		return err
	}

	contextFiles, err := buildContextFiles(ctx, params.ContextFiles, params.TobyConfig, sbx, run)
	if err != nil {
		return err
	}
	manager := &control.Manager{RepositoryResolver: sbx, ContextFiles: contextFiles}
	socketPath := control.HostSocketPath(env["XDG_RUNTIME_DIR"], os.Getpid())
	server, err := control.Listen(ctx, socketPath, manager.Handle)
	if err != nil {
		return err
	}
	defer server.Close()

	tobyBinary, err := os.Executable()
	if err != nil {
		return err
	}
	commandMounts := sbx.CommandMounts(toolset, socketPath, tobyBinary)
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

func buildContextFiles(ctx context.Context, service *contextfiles.Service, cfg *tobyconfig.Service, sbx *sandbox.Sandbox, run *tool.RunContext) ([]control.ContextFile, error) {
	if service == nil {
		return nil, fmt.Errorf("context files service is not configured")
	}
	session := service.NewSession(sbx.TobyContextDir())
	run.ContextFiles = session
	if err := registerContextFiles(ctx, session, cfg, run); err != nil {
		return nil, err
	}
	files := session.Files()
	contextFiles := make([]control.ContextFile, 0, len(files))
	for _, file := range files {
		contextFiles = append(contextFiles, control.ContextFile{Path: file.Path, Mode: file.Mode, Data: file.Data})
	}
	return contextFiles, nil
}

func registerContextFiles(ctx context.Context, session *contextfiles.Session, cfg *tobyconfig.Service, run *tool.RunContext) error {
	if err := contextfiles.RegisterAgentInstructions(session); err != nil {
		return err
	}
	if cfg != nil {
		if err := cfg.RegisterContextFiles(session); err != nil {
			return err
		}
	}
	return run.Toolset.RegisterContextFiles(ctx, run)
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

func newSandboxInitCommand() *cobra.Command {
	return &cobra.Command{
		Use:                "init -- COMMAND [ARG...]",
		Short:              "Initialize Toby sandbox context and exec a command.",
		DisableFlagParsing: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(args) > 0 && args[0] == "--" {
				args = args[1:]
			}
			if len(args) == 0 {
				return exitcode.New(2, "init requires a command after --")
			}
			client, err := sandboxControlClient()
			if err != nil {
				return err
			}
			result, err := client.ContextFiles()
			if err != nil {
				return err
			}
			contextDir, err := control.DefaultContextDir()
			if err != nil {
				return err
			}
			if err := writeContextFiles(contextDir, result.Files); err != nil {
				return err
			}
			path, err := exec.LookPath(args[0])
			if err != nil {
				return err
			}
			return syscall.Exec(path, args, os.Environ())
		},
	}
}

func writeContextFiles(contextDir string, files []control.ContextFile) error {
	if err := os.RemoveAll(contextDir); err != nil {
		return err
	}
	for _, file := range files {
		path, err := cleanContextPath(file.Path)
		if err != nil {
			return err
		}
		mode := fs.FileMode(file.Mode & 0o777)
		if mode == 0 {
			mode = 0o400
		}
		target := filepath.Join(contextDir, filepath.FromSlash(path))
		if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
			return err
		}
		if err := os.WriteFile(target, file.Data, mode); err != nil {
			return err
		}
		if err := os.Chmod(target, mode); err != nil {
			return err
		}
	}
	return nil
}

func cleanContextPath(path string) (string, error) {
	path = strings.TrimSpace(path)
	if path == "" || path == "." || !fs.ValidPath(path) {
		return "", fmt.Errorf("invalid context file path: %q", path)
	}
	return path, nil
}

func newSandboxMCPCommand() *cobra.Command {
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
	path, err := control.DefaultSocketPath()
	if err != nil {
		return nil, err
	}
	if _, err := os.Stat(path); err != nil {
		return nil, fmt.Errorf("toby-sandbox commands must run inside a Toby sandbox: %s is not available", path)
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
