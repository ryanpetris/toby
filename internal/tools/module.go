package tools

import (
	"petris.dev/toby/internal/tools/claude"
	"petris.dev/toby/internal/tools/codex"
	"petris.dev/toby/internal/tools/copilot"
	"petris.dev/toby/internal/tools/docker"
	"petris.dev/toby/internal/tools/emdash"
	"petris.dev/toby/internal/tools/forgejocli"
	"petris.dev/toby/internal/tools/githubcli"
	"petris.dev/toby/internal/tools/gitlabcli"
	"petris.dev/toby/internal/tools/grok"
	"petris.dev/toby/internal/tools/npm"
	"petris.dev/toby/internal/tools/opencode"
	"petris.dev/toby/internal/tools/speckit"
	"petris.dev/toby/internal/tools/t3"
	"petris.dev/toby/internal/tools/toolutil"
	"petris.dev/toby/internal/tools/uv"

	"go.uber.org/fx"
)

func Module() fx.Option {
	return fx.Module(
		"tools",
		toolutil.Module,
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
