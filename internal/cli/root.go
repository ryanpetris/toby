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

	"petris.dev/toby/internal/config"
	"petris.dev/toby/internal/contextfiles"
	"petris.dev/toby/internal/contextinit"
	"petris.dev/toby/internal/control"
	"petris.dev/toby/internal/exitcode"
	"petris.dev/toby/internal/hostmanager"
	"petris.dev/toby/internal/mcpserver"
	"petris.dev/toby/internal/sandbox"
	"petris.dev/toby/internal/sandboxbinary"
	"petris.dev/toby/internal/sandboxmanager"
	"petris.dev/toby/internal/tobyconfig"
	"petris.dev/toby/internal/tool"
	"petris.dev/toby/internal/version"
	"petris.dev/toby/internal/warning"

	"github.com/spf13/cobra"
)

type Params struct {
	Registry       *tool.Registry
	SandboxFactory sandbox.Factory
	Paths          config.Paths
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
	var configPath string
	cmd := &cobra.Command{
		Use:           "toby",
		Short:         "Run Toby Sandbox development environments.",
		Long:          "Toby Sandbox runs development tools inside private-home development sandboxes.",
		Version:       version.String(),
		SilenceUsage:  true,
		SilenceErrors: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			if configPath == "" {
				return cmd.Help()
			}
			launch, err := buildConfiguredLaunch(params, configPath, args)
			if err != nil {
				return err
			}
			return runSandboxCommand(cmd.Context(), params, &launch.Options, launch.Extra, launch.RequestedTools, launch.Primary)
		},
	}
	cmd.SetOut(stdout)
	cmd.SetErr(stderr)
	cmd.SetArgs(params.Args)
	cmd.SetVersionTemplate("{{.Version}}\n")
	cmd.Flags().StringVar(&configPath, "config", "", "Launch from a YAML or JSON configuration file.")

	cmd.AddCommand(newSandboxCommand(params))
	for _, item := range params.Registry.LaunchTools() {
		cmd.AddCommand(newLaunchCommand(params, item))
	}
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
			return runSandboxCommand(cmd.Context(), params, &parsed.Options, parsed.Extra, parsed.RequestedTools, primary.Name())
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

func runSandboxCommand(ctx context.Context, params Params, opts *tool.CommandOptions, extra, requestedTools []string, primary string) error {
	effectiveOpts := applySandboxDefaults(opts, params.TobyConfig)
	opts = &effectiveOpts
	sbx, err := params.SandboxFactory.FromOptions(opts)
	if err != nil {
		return err
	}
	defer sbx.Cleanup()

	toolset, err := params.Registry.Build(requestedTools, primary)
	if err != nil {
		return err
	}
	toolset.SetToolStates(opts.ToolStates)
	warnHostToolState(params.Stderr, opts.SuppressWarnings, toolset)
	if err := toolset.HostInit(ctx, opts); err != nil {
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
	endpoint := sbx.HostControlEndpoint()
	endpoint.BinarySource = sandboxbinary.SourceBytes
	server, err := control.ListenEndpoint(ctx, endpoint, manager.HandleConnection)
	if err != nil {
		return err
	}
	defer server.Close()
	sbx.SetupControlEndpoint(env, server.Endpoint)

	sandboxManagerExit := newSandboxManagerExit()
	go func() {
		code, err := sbx.Run(ctx, sandbox.RunSpec{Argv: sandboxManagerArgv(sbx), Toolset: toolset, Env: env})
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
	} else {
		runErr = toolset.Launch(ctx, run)
	}
	if termErr := terminateSandboxManager(ctx, sandboxClient, sandboxManagerExit); runErr == nil && termErr != nil {
		return termErr
	}
	return runErr
}

func applySandboxDefaults(opts *tool.CommandOptions, config *tobyconfig.Service) tool.CommandOptions {
	if opts == nil {
		opts = &tool.CommandOptions{}
	}
	result := *opts
	defaults := config.Sandbox()
	result.ToolStates = defaults.Tools.Clone()
	result.ToolStates.Merge(opts.ToolStates)
	result.SuppressWarnings = defaults.SuppressWarnings.Clone()
	result.SuppressWarnings.Merge(opts.SuppressWarnings)
	if result.BubblewrapRoot == "" {
		result.BubblewrapRoot = defaults.Runtime.Bubblewrap.Root
	}
	if result.SandboxRuntime == "" {
		result.SandboxRuntime = defaults.Runtime.Default
	}
	if result.SandboxRuntime != "docker" {
		return result
	}
	if result.DockerImage == "" {
		result.DockerImage = defaults.Runtime.Docker.Image
	}
	if result.DockerHome == "" {
		result.DockerHome = defaults.Runtime.Docker.Home
	}
	if result.DockerProjects == "" {
		result.DockerProjects = defaults.Runtime.Docker.Projects
	}
	return result
}

func warnHostToolState(stderr io.Writer, suppression warning.Suppression, toolset *tool.Toolset) {
	names := toolset.HostStateToolNames()
	if len(names) == 0 {
		return
	}
	warning.Fprintf(stderr, suppression, warning.ToolHostState, "using host tool state for %s; running multiple sandbox instances with the same host tool state can corrupt tool databases.", strings.Join(names, ", "))
}

func sandboxManagerArgv(sbx sandbox.Instance) []string {
	return []string{
		"/bin/sh", "-c",
		`set -e; mkdir -p /tmp/toby/bin; curl -fsSL -H "Authorization: Bearer ${TOBY_CONTROL_TOKEN}" "${TOBY_BINARY_URL}" -o /tmp/toby/bin/toby; chmod 755 /tmp/toby/bin/toby; exec "$@"`,
		"toby-startup", sbx.TobyBinaryPath(), "sandbox", "manager",
	}
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

func initSandboxContext(ctx context.Context, params Params, sbx sandbox.Instance, run *tool.RunContext, client *hostmanager.SandboxClient) error {
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
		Use:   "commit REPOSITORY -m MESSAGE [--amend]",
		Short: "Commit staged files in a visible repository using host Git.",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			message, _ := cmd.Flags().GetString("message")
			if message == "" {
				return exitcode.New(2, "commit message is required")
			}
			amend, _ := cmd.Flags().GetBool("amend")
			client, err := sandboxControlClient()
			if err != nil {
				return err
			}
			result, err := client.GitCommit(args[0], message, amend)
			if err != nil {
				return err
			}
			return writeGitResult(cmd, result)
		},
	}
	commit.Flags().StringP("message", "m", "", "Commit message passed to git commit -m.")
	commit.Flags().Bool("amend", false, "Amend the previous commit.")
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
	push := &cobra.Command{
		Use:   "push REPOSITORY BRANCH [ORIGIN]",
		Short: "Push one branch from a visible repository using host Git.",
		Args:  cobra.RangeArgs(2, 3),
		RunE: func(cmd *cobra.Command, args []string) error {
			origin := ""
			if len(args) == 3 {
				origin = args[2]
			}
			tags, _ := cmd.Flags().GetBool("tags")
			client, err := sandboxControlClient()
			if err != nil {
				return err
			}
			result, err := client.GitPush(args[0], args[1], origin, tags)
			if err != nil {
				return err
			}
			return writeGitResult(cmd, result)
		},
	}
	push.Flags().Bool("tags", false, "Push all tags with git push --tags.")
	cmd.AddCommand(push)
	rebase := &cobra.Command{
		Use:   "rebase REPOSITORY BASE|--continue|--abort",
		Short: "Start, continue, or abort a rebase using host Git.",
		Args:  cobra.RangeArgs(1, 2),
		RunE: func(cmd *cobra.Command, args []string) error {
			continueRebase, _ := cmd.Flags().GetBool("continue")
			abort, _ := cmd.Flags().GetBool("abort")
			if continueRebase && abort {
				return exitcode.New(2, "rebase accepts only one of --continue or --abort")
			}
			base := ""
			if continueRebase || abort {
				if len(args) != 1 {
					return exitcode.New(2, "rebase --continue and --abort accept only REPOSITORY")
				}
			} else {
				if len(args) != 2 {
					return exitcode.New(2, "rebase base is required")
				}
				base = args[1]
			}
			client, err := sandboxControlClient()
			if err != nil {
				return err
			}
			result, err := client.GitRebase(args[0], base, continueRebase, abort)
			if err != nil {
				return err
			}
			return writeGitResult(cmd, result)
		},
	}
	rebase.Flags().Bool("continue", false, "Continue an in-progress rebase.")
	rebase.Flags().Bool("abort", false, "Abort an in-progress rebase.")
	cmd.AddCommand(rebase)
	tag := &cobra.Command{
		Use:   "tag REPOSITORY TAG -m MESSAGE [TARGET]",
		Short: "Create an annotated tag in a visible repository using host Git.",
		Args:  cobra.RangeArgs(2, 3),
		RunE: func(cmd *cobra.Command, args []string) error {
			message, _ := cmd.Flags().GetString("message")
			if message == "" {
				return exitcode.New(2, "tag message is required")
			}
			target := ""
			if len(args) == 3 {
				target = args[2]
			}
			client, err := sandboxControlClient()
			if err != nil {
				return err
			}
			result, err := client.GitTag(args[0], args[1], message, target)
			if err != nil {
				return err
			}
			return writeGitResult(cmd, result)
		},
	}
	tag.Flags().StringP("message", "m", "", "Tag message passed to git tag -m.")
	cmd.AddCommand(tag)
	return cmd
}

func sandboxControlClient() (*control.Client, error) {
	endpoint, err := control.DefaultEndpoint()
	if err != nil {
		return nil, err
	}
	return control.NewEndpointClient(endpoint), nil
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
