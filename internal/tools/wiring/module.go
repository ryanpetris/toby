// Package wiring composes every concrete built-in tool into the process-wide Fx
// graph. The registry selects active tools per launch without rebuilding the
// dependency-injection graph.
package wiring

import (
	"petris.dev/toby/internal/tools"
	"petris.dev/toby/internal/tools/builtin/claude"
	"petris.dev/toby/internal/tools/builtin/codex"
	"petris.dev/toby/internal/tools/builtin/copilot"
	"petris.dev/toby/internal/tools/builtin/dcode"
	"petris.dev/toby/internal/tools/builtin/docker"
	"petris.dev/toby/internal/tools/builtin/emdash"
	"petris.dev/toby/internal/tools/builtin/exectool"
	"petris.dev/toby/internal/tools/builtin/forgejocli"
	"petris.dev/toby/internal/tools/builtin/githubcli"
	"petris.dev/toby/internal/tools/builtin/gitlabcli"
	"petris.dev/toby/internal/tools/builtin/grok"
	"petris.dev/toby/internal/tools/builtin/npm"
	"petris.dev/toby/internal/tools/builtin/opencode"
	"petris.dev/toby/internal/tools/builtin/speckit"
	"petris.dev/toby/internal/tools/builtin/t3"
	"petris.dev/toby/internal/tools/builtin/uv"
	"petris.dev/toby/internal/tools/kit"

	"go.uber.org/fx"
)

// entry pairs a tool's self-declared metadata with the fx module that builds it.
type entry struct {
	Meta   tools.Metadata
	Module fx.Option
}

// entries enumerates every built-in tool. This is the only list of tools in the
// codebase; each row references a tool package's own Meta and Module.
var entries = []entry{
	{exectool.Meta, exectool.Module},
	{npm.Meta, npm.Module},
	{docker.Meta, docker.Module},
	{claude.Meta, claude.Module},
	{copilot.Meta, copilot.Module},
	{codex.Meta, codex.Module},
	{dcode.Meta, dcode.Module},
	{t3.Meta, t3.Module},
	{opencode.Meta, opencode.Module},
	{uv.Meta, uv.Module},
	{emdash.Meta, emdash.Module},
	{grok.Meta, grok.Module},
	{speckit.Meta, speckit.Module},
	{githubcli.Meta, githubcli.Module},
	{gitlabcli.Meta, gitlabcli.Module},
	{forgejocli.Meta, forgejocli.Module},
}

// Module provides the shared tool kit and every concrete built-in tool exactly
// once. Per-launch selection happens in tools.Registry.
var Module = func() fx.Option {
	options := make([]fx.Option, 0, len(entries)+1)
	options = append(options, kit.Module)
	for _, entry := range entries {
		options = append(options, entry.Module)
	}
	return fx.Module("tools", options...)
}()
