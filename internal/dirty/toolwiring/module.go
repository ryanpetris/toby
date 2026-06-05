package toolwiring

import (
	"fmt"

	"petris.dev/toby/tools"
	"petris.dev/toby/tools/builtin/claude"
	"petris.dev/toby/tools/builtin/codex"
	"petris.dev/toby/tools/builtin/copilot"
	"petris.dev/toby/tools/builtin/docker"
	"petris.dev/toby/tools/builtin/emdash"
	"petris.dev/toby/tools/builtin/exectool"
	"petris.dev/toby/tools/builtin/forgejocli"
	"petris.dev/toby/tools/builtin/githubcli"
	"petris.dev/toby/tools/builtin/gitlabcli"
	"petris.dev/toby/tools/builtin/grok"
	"petris.dev/toby/tools/builtin/npm"
	"petris.dev/toby/tools/builtin/opencode"
	"petris.dev/toby/tools/builtin/speckit"
	"petris.dev/toby/tools/builtin/t3"
	"petris.dev/toby/tools/builtin/uv"
	"petris.dev/toby/tools/kit"

	"go.uber.org/fx"
)

func Module() fx.Option {
	return fx.Module(
		"tools",
		kit.Module,
		exectool.Module,
		npm.Module,
		docker.Module,
		claude.Module,
		copilot.Module,
		codex.Module,
		t3.Module,
		opencode.Module,
		uv.Module,
		emdash.Module,
		grok.Module,
		speckit.Module,
		githubcli.Module,
		gitlabcli.Module,
		forgejocli.Module,
	)
}

func PlanningModule() fx.Option {
	return fx.Module("tools.planning", fx.Provide(newPlanningTools))
}

func SelectedModule(names []string) (fx.Option, error) {
	modules := []fx.Option{kit.Module}
	seen := map[string]bool{}
	for _, name := range names {
		if seen[name] {
			continue
		}
		module, ok := toolModules[name]
		if !ok {
			return nil, fmt.Errorf("unknown tool: %s", name)
		}
		seen[name] = true
		modules = append(modules, module)
	}
	return fx.Module("tools.selected", modules...), nil
}

var toolModules = map[string]fx.Option{
	tools.ExecToolName:       exectool.Module,
	tools.NpmToolName:        npm.Module,
	tools.DockerToolName:     docker.Module,
	tools.ClaudeToolName:     claude.Module,
	tools.CopilotToolName:    copilot.Module,
	tools.CodexToolName:      codex.Module,
	tools.T3ToolName:         t3.Module,
	tools.OpenCodeToolName:   opencode.Module,
	tools.UvToolName:         uv.Module,
	tools.EmdashToolName:     emdash.Module,
	tools.GrokToolName:       grok.Module,
	tools.SpeckitToolName:    speckit.Module,
	tools.GitHubCliToolName:  githubcli.Module,
	tools.GitLabCliToolName:  gitlabcli.Module,
	tools.ForgejoCliToolName: forgejocli.Module,
}
