package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"petris.dev/toby/internal/contextfiles"
	"petris.dev/toby/internal/contextinit"
	"petris.dev/toby/internal/control"
	"petris.dev/toby/internal/exitcode"
	"petris.dev/toby/internal/hostmanager"
	"petris.dev/toby/internal/mcpserver"
	"petris.dev/toby/internal/sandbox"
	"petris.dev/toby/internal/sandboxmanager"
	"petris.dev/toby/internal/tobyconfig"
	"petris.dev/toby/internal/tool"

	"github.com/spf13/cobra"
)

type Params struct {
	Registry       *tool.Registry
	SandboxFactory sandbox.Factory
	ContextFiles   *contextfiles.Service
	ContextInit    []contextinit.Registration
	HostManager    *hostmanager.HostManager
	SandboxManager *sandboxmanager.Runner
	MCPServer      *mcpserver.Runner
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

	cmd.AddCommand(newSandboxCommand(params))
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

	exits := newCommandExits()
	ready := make(chan sandboxManagerReady, 1)
	if params.HostManager == nil {
		return fmt.Errorf("host manager is not configured")
	}
	manager := params.HostManager
	manager.RepositoryResolver = sbx
	manager.CommandExit = exits.complete
	manager.ContextInit = func(ctx context.Context, client *hostmanager.SandboxClient) error {
		return initSandboxContext(ctx, params, sbx, run, client)
	}
	manager.SandboxReady = func(client *hostmanager.SandboxClient, err error) {
		ready <- sandboxManagerReady{client: client, err: err}
	}
	socketPath := control.HostSocketPath(env["XDG_RUNTIME_DIR"], os.Getpid())
	server, err := control.ListenConnections(ctx, socketPath, manager.HandleConnection)
	if err != nil {
		return err
	}
	defer server.Close()

	tobyBinary, err := os.Executable()
	if err != nil {
		return err
	}
	commandMounts := sbx.CommandMounts(toolset, socketPath, tobyBinary)
	sandboxManagerExit := newSandboxManagerExit()
	go func() {
		code, err := sbx.Run(ctx, []string{sbx.TobyBinaryPath(), "sandbox", "manager"}, commandMounts, env, tool.ExecOptions{})
		sandboxManagerExit.set(sandboxManagerProcessResult{exitCode: code, err: err})
	}()

	var sandboxClient *hostmanager.SandboxClient
	select {
	case result := <-ready:
		if result.err != nil {
			return waitSandboxManagerAfterError(ctx, sandboxManagerExit, result.client, result.err)
		}
		sandboxClient = result.client
	case <-sandboxManagerExit.done:
		result := sandboxManagerExit.result()
		if result.err != nil {
			return result.err
		}
		return exitcode.New(result.exitCode, "sandbox manager exited before context init")
	case <-ctx.Done():
		return ctx.Err()
	}
	executor := &sandboxManagerExecutor{client: sandboxClient, exits: exits, sandboxManagerExit: sandboxManagerExit}
	run.Exec = func(ctx context.Context, argv []string, options tool.ExecOptions) (int, error) {
		return executor.run(ctx, argv, options, false)
	}
	run.Launch = func(ctx context.Context, argv []string, options tool.ExecOptions) (int, error) {
		return executor.run(ctx, argv, options, true)
	}
	var runErr error
	if err := toolset.SandboxInit(ctx, run); err != nil {
		runErr = err
	} else if execMode {
		runErr = tool.RunCommand(ctx, run.Launch, commandOrShell(extra, env), tool.ExecOptions{})
	} else {
		runErr = toolset.Launch(ctx, run)
	}
	if termErr := terminateSandboxManager(ctx, sandboxClient, sandboxManagerExit); runErr == nil && termErr != nil {
		return termErr
	}
	return runErr
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

type sandboxManagerReady struct {
	client *hostmanager.SandboxClient
	err    error
}

type sandboxManagerProcessResult struct {
	exitCode int
	err      error
}

type sandboxManagerExit struct {
	done chan struct{}
	once sync.Once
	mu   sync.Mutex
	res  sandboxManagerProcessResult
}

func newSandboxManagerExit() *sandboxManagerExit {
	return &sandboxManagerExit{done: make(chan struct{})}
}

func (s *sandboxManagerExit) set(result sandboxManagerProcessResult) {
	s.mu.Lock()
	s.res = result
	s.mu.Unlock()
	s.once.Do(func() { close(s.done) })
}

func (s *sandboxManagerExit) result() sandboxManagerProcessResult {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.res
}

type commandExits struct {
	mu      sync.Mutex
	waiting map[string]chan control.CommandExitParams
}

func newCommandExits() *commandExits {
	return &commandExits{waiting: map[string]chan control.CommandExitParams{}}
}

func (e *commandExits) watch(commandID string) chan control.CommandExitParams {
	ch := make(chan control.CommandExitParams, 1)
	e.mu.Lock()
	e.waiting[commandID] = ch
	e.mu.Unlock()
	return ch
}

func (e *commandExits) unwatch(commandID string) {
	e.mu.Lock()
	delete(e.waiting, commandID)
	e.mu.Unlock()
}

func (e *commandExits) complete(params control.CommandExitParams) {
	e.mu.Lock()
	ch := e.waiting[params.CommandID]
	delete(e.waiting, params.CommandID)
	e.mu.Unlock()
	if ch != nil {
		ch <- params
	}
}

type sandboxManagerExecutor struct {
	client             *hostmanager.SandboxClient
	exits              *commandExits
	sandboxManagerExit *sandboxManagerExit
}

func (e *sandboxManagerExecutor) run(ctx context.Context, argv []string, options tool.ExecOptions, foreground bool) (int, error) {
	commandID, err := control.NewCommandID()
	if err != nil {
		return 1, err
	}
	exitCh := e.exits.watch(commandID)
	if err := e.client.CommandRun(ctx, control.CommandRunParams{CommandID: commandID, Argv: argv, Foreground: foreground, HideOutput: options.HideOutput}); err != nil {
		e.exits.unwatch(commandID)
		return 1, err
	}
	select {
	case result := <-exitCh:
		if result.Error != "" {
			return result.ExitCode, errors.New(result.Error)
		}
		return result.ExitCode, nil
	case <-e.sandboxManagerExit.done:
		result := e.sandboxManagerExit.result()
		if result.err != nil {
			return 1, result.err
		}
		return result.exitCode, fmt.Errorf("sandbox manager exited before command completed")
	case <-ctx.Done():
		e.exits.unwatch(commandID)
		return 130, ctx.Err()
	}
}

type sandboxManagerContextSink struct {
	ctx    context.Context
	client *hostmanager.SandboxClient
	base   string
}

func (s *sandboxManagerContextSink) AddFile(path string, data []byte, mode uint32) error {
	target := filepath.Join(s.base, filepath.FromSlash(path))
	return s.client.FileCreate(s.ctx, target, data, mode)
}

func initSandboxContext(ctx context.Context, params Params, sbx *sandbox.Sandbox, run *tool.RunContext, client *hostmanager.SandboxClient) error {
	if params.ContextFiles == nil {
		return fmt.Errorf("context files service is not configured")
	}
	contextDir := sbx.TobyContextDir()
	if err := client.FileDelete(ctx, contextDir, true); err != nil {
		return err
	}
	if err := client.FileMkdir(ctx, contextDir, 0o700); err != nil {
		return err
	}
	sink := &sandboxManagerContextSink{ctx: ctx, client: client, base: contextDir}
	run.ContextFiles = params.ContextFiles.NewEmittingSession(contextDir, sink)
	for _, service := range contextinit.Ordered(params.ContextInit) {
		if err := service.InitContext(ctx, run); err != nil {
			return err
		}
	}
	return nil
}

func waitSandboxManagerAfterError(ctx context.Context, exit *sandboxManagerExit, client *hostmanager.SandboxClient, err error) error {
	_ = terminateSandboxManager(ctx, client, exit)
	return err
}

func terminateSandboxManager(ctx context.Context, client *hostmanager.SandboxClient, exit *sandboxManagerExit) error {
	if client != nil {
		if err := client.Terminate(ctx); err != nil {
			return err
		}
	}
	select {
	case <-exit.done:
		result := exit.result()
		if result.err != nil {
			return result.err
		}
		if result.exitCode != 0 {
			return exitcode.Code(result.exitCode)
		}
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func newSandboxCommand(params Params) *cobra.Command {
	cmd := &cobra.Command{
		Use:    "sandbox",
		Short:  "Run Toby sandbox internals.",
		Hidden: os.Getenv("TOBY_SANDBOX") != "1",
	}
	cmd.AddCommand(newSandboxManagerCommand(params.SandboxManager))
	cmd.AddCommand(newSandboxMCPCommand(params.MCPServer))
	cmd.AddCommand(newSandboxGitCommand())
	return cmd
}

func newSandboxManagerCommand(runner *sandboxmanager.Runner) *cobra.Command {
	return &cobra.Command{
		Use:   "manager",
		Short: "Run the Toby sandbox manager.",
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(args) != 0 {
				return exitcode.New(2, "manager does not accept arguments")
			}
			if runner == nil {
				return fmt.Errorf("sandbox manager runner is not configured")
			}
			return runner.Run(cmd.Context(), "")
		},
	}
}

func newSandboxMCPCommand(runner *mcpserver.Runner) *cobra.Command {
	return &cobra.Command{
		Use:   "mcp",
		Short: "Run the Toby MCP server inside a sandbox.",
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(args) != 0 {
				return exitcode.New(2, "mcp does not accept arguments")
			}
			if runner == nil {
				return fmt.Errorf("mcp server runner is not configured")
			}
			return runner.Run(cmd.Context(), "")
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
