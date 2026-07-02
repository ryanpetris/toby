// Package dcode provides the Deep Agents Code CLI agent tool: it installs
// deepagents-code via uv and launches dcode with Toby's generated MCP config.
package dcode

import (
	"context"
	"strings"

	"petris.dev/toby/config/session"
	contextfiles "petris.dev/toby/context/files"
	"petris.dev/toby/diagnostic/exitcode"
	appconfig "petris.dev/toby/internal/config/app"
	"petris.dev/toby/internal/control"
	dcodeconfig "petris.dev/toby/internal/tools/builtin/dcode/config"
	"petris.dev/toby/internal/tools/builtin/uv"
	"petris.dev/toby/sandbox"
	"petris.dev/toby/tools"
	"petris.dev/toby/tools/helpers"
	"petris.dev/toby/tools/kit"

	"go.uber.org/fx"
)

var Module = fx.Module("tools.dcode", fx.Provide(Provide))

// Name is this tool's canonical identifier.
const Name = "dcode"

const (
	providerTypeAnthropic = "anthropic"
	providerTypeOpenAI    = "openai"
)

// Meta is this tool's declarative identity. It runs after uv via its dependency.
var Meta = tools.Metadata{
	Name:          Name,
	LaunchHelp:    "Launch Deep Agents Code",
	Group:         tools.GroupAI,
	ContextGroups: []string{tools.GroupAI, tools.GroupSystem, tools.GroupVCS},
	Dependencies:  []string{uv.Name},
}

type Params struct {
	fx.In

	SessionConfig *sessionconfig.Holder
	Sandbox       sandbox.Service
	ContextFiles  *contextfiles.Service
	Config        *appconfig.Service
}

type Result struct {
	fx.Out

	Service tools.Tool `group:"tools"`
}

func Provide(params Params) Result {
	svc := &deepAgentsTool{
		Simple: kit.NewSimple(
			params.Sandbox,
			tools.Base{Metadata: Meta},
			nil,
			nil,
		),
		sessionConfig: params.SessionConfig,
		contextFiles:  params.ContextFiles,
		config:        params.Config,
	}
	return Result{Service: svc}
}

type deepAgentsTool struct {
	*kit.Simple
	sessionConfig *sessionconfig.Holder
	contextFiles  *contextfiles.Service
	config        *appconfig.Service
	yolo          bool
}

var _ tools.Tool = (*deepAgentsTool)(nil)

func (t *deepAgentsTool) PrepareHost(ctx context.Context, opts *tools.Options) error {
	t.yolo = t.config.YoloEnabled()

	return t.Simple.PrepareHost(ctx, opts)
}

func (t *deepAgentsTool) RegisterContextFiles(ctx context.Context, opts tools.ContextOptions) error {
	return dcodeconfig.RegisterContextFiles(t.contextFiles.Registrar(ctx), t.sessionConfig.Get())
}

func (t *deepAgentsTool) Install(ctx context.Context, force bool) error {
	if !force {
		exists, err := helpers.CommandExists(ctx, t.Sandbox.Exec, sandbox.ExecOptions{HideOutput: true}, "dcode")
		if err != nil || exists {
			return err
		}
	}

	command := []string{"uv", "tool", "install", "deepagents-code"}
	if force {
		command = append(command, "--force")
	}
	code, err := t.Sandbox.Exec(ctx, command, sandbox.ExecOptions{})
	if err != nil {
		return err
	}
	if code != 0 {
		return exitcode.New(code, "uv tool install deepagents-code failed")
	}
	return nil
}

func (t *deepAgentsTool) LaunchCommand(ctx context.Context, extra []string) ([]string, error) {
	// The agent-file write and provider configuration are daemon-side setup; they run
	// here (before returning the argv) so the container is ready when the client execs.
	if !hasAgentArg(extra) {
		if err := t.writeTobyAgent(ctx); err != nil {
			return nil, err
		}
	}
	if err := t.configureSelectedProvider(ctx, extra); err != nil {
		return nil, err
	}

	argv := []string{"dcode", "--mcp-config", dcodeconfig.MCPConfigPath}
	if !hasAgentArg(extra) {
		argv = append(argv, "--agent", "toby")
	}
	if t.yolo {
		argv = append(argv, "-y")
	}
	argv = append(argv, extra...)
	return argv, nil
}

func (t *deepAgentsTool) writeTobyAgent(ctx context.Context) error {
	if err := t.Sandbox.MkdirOwned(ctx, dcodeconfig.AgentDir, 0o755, control.HostUser, control.HostGroup); err != nil {
		return err
	}

	return t.Sandbox.AddFileOwned(ctx, dcodeconfig.InstructionsPath, dcodeconfig.Instructions(t.sessionConfig.Get()), 0o644, control.HostUser, control.HostGroup)
}

func (t *deepAgentsTool) configureSelectedProvider(ctx context.Context, args []string) error {
	provider, ok := selectedModelProvider(args)
	if !ok {
		return nil
	}

	var keyVar, urlVar string
	switch provider {
	case providerTypeAnthropic:
		keyVar = "DEEPAGENTS_CODE_ANTHROPIC_API_KEY"
		urlVar = "DEEPAGENTS_CODE_ANTHROPIC_BASE_URL"
	case providerTypeOpenAI:
		keyVar = "DEEPAGENTS_CODE_OPENAI_API_KEY"
		urlVar = "DEEPAGENTS_CODE_OPENAI_BASE_URL"
	default:
		return nil
	}

	url, ok := singleProviderURL(t.sessionConfig.Get().Providers, provider)
	if !ok {
		return nil
	}
	if err := t.Sandbox.SetEnvironment(ctx, keyVar, "toby"); err != nil {
		return err
	}
	return t.Sandbox.SetEnvironment(ctx, urlVar, url)
}

func selectedModelProvider(args []string) (string, bool) {
	for i, arg := range args {
		if arg == "--" {
			return "", false
		}
		if arg == "--model" || arg == "-M" {
			if i+1 >= len(args) {
				return "", false
			}
			provider, _, ok := strings.Cut(args[i+1], ":")
			return provider, ok
		}
		if model, ok := strings.CutPrefix(arg, "--model="); ok {
			provider, _, ok := strings.Cut(model, ":")
			return provider, ok
		}
		if model, ok := strings.CutPrefix(arg, "-M"); ok && model != "" {
			provider, _, ok := strings.Cut(strings.TrimPrefix(model, "="), ":")
			return provider, ok
		}
	}
	return "", false
}

func singleProviderURL(providers []sessionconfig.Provider, providerType string) (string, bool) {
	var url string
	for _, provider := range providers {
		if provider.Type != providerType {
			continue
		}
		if url != "" {
			return "", false
		}
		url = provider.URL
	}
	return url, url != ""
}

func hasAgentArg(args []string) bool {
	for _, arg := range args {
		if arg == "--" {
			return false
		}
		if arg == "--agent" || arg == "-a" || strings.HasPrefix(arg, "--agent=") || strings.HasPrefix(arg, "-a=") || (strings.HasPrefix(arg, "-a") && arg != "-a") {
			return true
		}
	}
	return false
}
