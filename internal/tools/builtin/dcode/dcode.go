// Package dcode provides the Deep Agents Code CLI agent tool: it installs
// deepagents-code via uv and launches dcode with Toby's generated MCP config.
package dcode

import (
	"context"
	"strings"

	appconfig "petris.dev/toby/internal/config/app"
	"petris.dev/toby/internal/config/session"
	"petris.dev/toby/internal/diagnostic/exitcode"
	"petris.dev/toby/internal/lifecycle"
	"petris.dev/toby/internal/sandbox"
	"petris.dev/toby/internal/toolfiles"
	"petris.dev/toby/internal/tools"
	dcodeconfig "petris.dev/toby/internal/tools/builtin/dcode/config"
	"petris.dev/toby/internal/tools/builtin/uv"
	"petris.dev/toby/internal/tools/helpers"
	"petris.dev/toby/internal/tools/kit"
)

// Name is this tool's canonical identifier.
const Name = "dcode"

const (
	providerTypeAnthropic = "anthropic"
	providerTypeOpenAI    = "openai"
)

// Meta is this tool's declarative identity. It runs after uv via its dependency.
var Meta = tools.Metadata{
	Name:          Name,
	DisplayName:   "Deep Agents Code",
	LaunchHelp:    "Launch Deep Agents Code",
	Group:         tools.GroupAI,
	ContextGroups: []string{tools.GroupAI, tools.GroupSystem, tools.GroupVCS},
	Dependencies:  []string{uv.Name},
}

// provide constructs the tool implementation.
func provide(params params) result {
	svc := &deepAgentsTool{
		Simple: kit.NewSimple(
			params.Sandbox,
			tools.Base{Metadata: Meta},
			[]string{".deepagents"},
			nil,
			nil,
		),
		sessionConfig: params.SessionConfig,
		config:        params.Config,
	}
	return result{Service: svc}
}

type deepAgentsTool struct {
	*kit.Simple
	sessionConfig *sessionconfig.Holder
	config        *appconfig.LaunchHolder
	yolo          bool
}

var _ tools.Tool = (*deepAgentsTool)(nil)
var _ lifecycle.LaunchPreparer = (*deepAgentsTool)(nil)
var _ toolfiles.Contributor = (*deepAgentsTool)(nil)

func (t *deepAgentsTool) PrepareHost(ctx context.Context, opts *tools.Options) error {
	current := t.config.Current()
	t.yolo = current != nil && current.Settings().YoloEnabled()

	return t.Simple.PrepareHost(ctx, opts)
}

func (t *deepAgentsTool) ToolFiles(ownership toolfiles.Ownership) ([]toolfiles.File, error) {
	cfg, err := t.sessionConfig.Config()
	if err != nil {
		return nil, err
	}

	return dcodeconfig.NativeFiles(Name, ownership, cfg)
}

func (t *deepAgentsTool) Install(ctx context.Context, force bool) error {
	if !force {
		exists, err := helpers.CommandExists(
			ctx,
			t.Sandbox.Exec,
			sandbox.ExecOptions{
				HideOutput: true,
				Status:     "Checking installation",
			},
			"dcode",
		)
		if err != nil || exists {
			return err
		}
	}

	command := []string{"uv", "tool", "install"}
	if force {
		command = append(command, "--upgrade")
	}
	command = append(command, "--prerelease", "allow", "deepagents-code")

	code, err := t.Sandbox.Exec(
		ctx,
		command,
		sandbox.ExecOptions{Status: "Installing"},
	)
	if err != nil {
		return err
	}
	if code != 0 {
		return exitcode.New(code, "uv tool install deepagents-code failed")
	}
	return nil
}

func (t *deepAgentsTool) Launch(ctx context.Context, extra []string) error {
	argv := []string{"dcode", "--mcp-config", dcodeconfig.NativeMCPPath}
	if !hasAgentArg(extra) {
		argv = append(argv, "--agent", "toby")
	}
	if t.yolo {
		argv = append(argv, "-y")
	}
	argv = append(argv, extra...)
	_, err := t.Sandbox.Exec(ctx, argv, sandbox.ExecOptions{Foreground: true})
	return err
}

// PrepareLaunch applies argument-dependent environment declarations before a
// native run freezes its Bubblewrap plan.
func (t *deepAgentsTool) PrepareLaunch(ctx context.Context, args []string) error {
	return t.configureSelectedProvider(ctx, args)
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

	url, credential, ok := singleProviderEndpoint(
		t.sessionConfig.Snapshot().Models,
		provider,
	)
	if !ok {
		return nil
	}
	if credential == "" {
		credential = "toby"
	}
	if err := t.Sandbox.SetEnvironment(ctx, keyVar, credential); err != nil {
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

func singleProviderEndpoint(providers []sessionconfig.ModelsEndpoint, providerType string) (string, string, bool) {
	var url, credential string
	for _, provider := range providers {
		if provider.Type != providerType {
			continue
		}
		if url != "" {
			return "", "", false
		}
		url = provider.URL
		credential = provider.Credential
	}
	return url, credential, url != ""
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
