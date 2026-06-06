// Package wiring composes the concrete tool implementations into fx modules: the
// planning-only metadata set (PlanningModule) and a launch-selected subset
// (SelectedModule). The `entries` list is the single place that enumerates the
// built-in tools; both the planning metadata and the name→module selection are
// derived from each tool's own self-declared Meta and Module.
package wiring

import (
	"fmt"

	"petris.dev/toby/internal/tools/builtin/claude"
	"petris.dev/toby/internal/tools/builtin/codex"
	"petris.dev/toby/internal/tools/builtin/copilot"
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
	"petris.dev/toby/tools"
	"petris.dev/toby/tools/kit"

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
		module, ok := toolModule(name)
		if !ok {
			return nil, fmt.Errorf("unknown tool: %s", name)
		}
		seen[name] = true
		modules = append(modules, module)
	}
	return fx.Module("tools.selected", modules...), nil
}

// toolModule returns the fx module that builds the named tool.
func toolModule(name string) (fx.Option, bool) {
	for _, e := range entries {
		if e.Meta.Name == name {
			return e.Module, true
		}
	}
	return nil, false
}
