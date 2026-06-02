package tools

import (
	"fmt"

	"petris.dev/toby/internal/tools/claude"
	"petris.dev/toby/internal/tools/codex"
	"petris.dev/toby/internal/tools/copilot"
	"petris.dev/toby/internal/tools/docker"
	"petris.dev/toby/internal/tools/emdash"
	"petris.dev/toby/internal/tools/exectool"
	"petris.dev/toby/internal/tools/forgejocli"
	"petris.dev/toby/internal/tools/githubcli"
	"petris.dev/toby/internal/tools/gitlabcli"
	"petris.dev/toby/internal/tools/grok"
	"petris.dev/toby/internal/tools/npm"
	"petris.dev/toby/internal/tools/opencode"
	"petris.dev/toby/internal/tools/speckit"
	"petris.dev/toby/internal/tools/t3"
	"petris.dev/toby/internal/tools/tool"
	"petris.dev/toby/internal/tools/toolutil"
	"petris.dev/toby/internal/tools/uv"

	"go.uber.org/fx"
)

func Module() fx.Option {
	return fx.Module(
		"tools",
		toolutil.Module,
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
	modules := []fx.Option{toolutil.Module}
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
	tool.ExecToolName:       exectool.Module,
	tool.NpmToolName:        npm.Module,
	tool.DockerToolName:     docker.Module,
	tool.ClaudeToolName:     claude.Module,
	tool.CopilotToolName:    copilot.Module,
	tool.CodexToolName:      codex.Module,
	tool.T3ToolName:         t3.Module,
	tool.OpenCodeToolName:   opencode.Module,
	tool.UvToolName:         uv.Module,
	tool.EmdashToolName:     emdash.Module,
	tool.GrokToolName:       grok.Module,
	tool.SpeckitToolName:    speckit.Module,
	tool.GitHubCliToolName:  githubcli.Module,
	tool.GitLabCliToolName:  gitlabcli.Module,
	tool.ForgejoCliToolName: forgejocli.Module,
}
