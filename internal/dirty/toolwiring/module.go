package toolwiring

import (
	"fmt"

	"petris.dev/toby/internal/dirty/tools/claude"
	"petris.dev/toby/internal/dirty/tools/codex"
	"petris.dev/toby/internal/dirty/tools/copilot"
	"petris.dev/toby/internal/dirty/tools/docker"
	"petris.dev/toby/internal/dirty/tools/emdash"
	"petris.dev/toby/internal/dirty/tools/exectool"
	"petris.dev/toby/internal/dirty/tools/forgejocli"
	"petris.dev/toby/internal/dirty/tools/githubcli"
	"petris.dev/toby/internal/dirty/tools/gitlabcli"
	"petris.dev/toby/internal/dirty/tools/grok"
	"petris.dev/toby/internal/dirty/tools/npm"
	"petris.dev/toby/internal/dirty/tools/opencode"
	"petris.dev/toby/internal/dirty/tools/speckit"
	"petris.dev/toby/internal/dirty/tools/t3"
	"petris.dev/toby/internal/dirty/tools/uv"
	"petris.dev/toby/tools"
	"petris.dev/toby/tools/toolutil"

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
