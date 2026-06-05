package wiring

// Planning tools: the fx output that exposes every built-in tool's declarative
// identity as a bare tools.Tool value (no execution services attached), sourced
// from the tool packages' own metadata via entries.

import (
	"petris.dev/toby/tools"

	"go.uber.org/fx"
)

type planningToolsResult struct {
	fx.Out

	Tools []tools.Tool `group:"tools,flatten"`
}

func newPlanningTools() planningToolsResult {
	metadatas := Metadata()
	result := planningToolsResult{Tools: make([]tools.Tool, 0, len(metadatas))}
	for _, metadata := range metadatas {
		result.Tools = append(result.Tools, tools.Base{Metadata: metadata})
	}
	return result
}

// Metadata returns each built-in tool's declarative identity, taken from the tool
// packages themselves (see entries). Slices are cloned so callers cannot mutate a
// tool's canonical metadata.
func Metadata() []tools.Metadata {
	result := make([]tools.Metadata, len(entries))
	for i, e := range entries {
		meta := e.Meta
		meta.ContextGroups = append([]string(nil), e.Meta.ContextGroups...)
		meta.Dependencies = append([]string(nil), e.Meta.Dependencies...)
		result[i] = meta
	}
	return result
}
