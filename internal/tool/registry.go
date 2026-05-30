package tool

import (
	"context"
	"fmt"
	"path/filepath"
	"sort"

	"go.uber.org/fx"
)

const FxToolGroup = "toby.tools"

type RegistryParams struct {
	fx.In

	Tools []Tool `group:"toby.tools"`
}

type Registry struct {
	tools map[string]Tool
}

func NewRegistry(params RegistryParams) (*Registry, error) {
	registry := &Registry{tools: make(map[string]Tool, len(params.Tools))}
	for _, item := range params.Tools {
		if item.Name() == "" {
			return nil, fmt.Errorf("registered tool must define a non-empty name")
		}
		if _, exists := registry.tools[item.Name()]; exists {
			return nil, fmt.Errorf("duplicate tool registration: %s", item.Name())
		}
		registry.tools[item.Name()] = item
	}
	return registry, nil
}

func (r *Registry) Get(name string) (Tool, bool) {
	tool, ok := r.tools[name]
	return tool, ok
}

func (r *Registry) MustGet(name string) Tool {
	return r.tools[name]
}

func (r *Registry) ToolNames() []string {
	names := make([]string, 0, len(r.tools))
	for name := range r.tools {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

func (r *Registry) LaunchTools() []Tool {
	seen := map[string]bool{}
	result := []Tool{}
	for _, group := range GroupOrder {
		names := append([]string(nil), ToolGroups[group]...)
		sort.Strings(names)
		for _, name := range names {
			if seen[name] {
				continue
			}
			seen[name] = true
			item, ok := r.Get(name)
			if !ok || item.LaunchHelp() == "" {
				continue
			}
			result = append(result, item)
		}
	}
	return result
}

func (r *Registry) Build(requested []string, primary string) (*Toolset, error) {
	seen := map[string]bool{}
	ordered := make([]Tool, 0, len(requested)+1)
	for _, name := range requested {
		item, ok := r.Get(name)
		if !ok {
			return nil, fmt.Errorf("unknown tool: %s", name)
		}
		if seen[name] {
			continue
		}
		seen[name] = true
		ordered = append(ordered, item)
	}
	var primaryTool Tool
	if primary != "" {
		var ok bool
		primaryTool, ok = r.Get(primary)
		if !ok {
			return nil, fmt.Errorf("unknown primary tool: %s", primary)
		}
		if !seen[primary] {
			ordered = append(ordered, primaryTool)
		}
	}
	return &Toolset{primary: primaryTool, ordered: ordered}, nil
}

func ExpandGroups(groups []string) []string {
	requested := map[string]bool{}
	for _, group := range groups {
		requested[group] = true
	}
	seen := map[string]bool{}
	result := []string{}
	for _, group := range GroupOrder {
		if !requested[group] {
			continue
		}
		names := append([]string(nil), ToolGroups[group]...)
		sort.Strings(names)
		for _, name := range names {
			if seen[name] {
				continue
			}
			seen[name] = true
			result = append(result, name)
		}
	}
	return result
}

type Toolset struct {
	primary    Tool
	ordered    []Tool
	toolStates ToolStateSettings
}

func (t *Toolset) SetToolStates(settings ToolStateSettings) {
	if t == nil {
		return
	}
	t.toolStates = settings.Clone()
}

func (t *Toolset) OrderedTools() []Tool {
	return append([]Tool(nil), t.ordered...)
}

func (t *Toolset) OrderedToolNames() []string {
	names := make([]string, len(t.ordered))
	for i, item := range t.ordered {
		names[i] = item.Name()
	}
	return names
}

func (t *Toolset) Has(name string) bool {
	for _, item := range t.ordered {
		if item.Name() == name {
			return true
		}
	}
	return false
}

func (t *Toolset) Binds() []Bind {
	var binds []Bind
	seen := map[Bind]bool{}
	for _, item := range t.ordered {
		state := t.toolStates.StateFor(item.Name())
		for _, bind := range item.Binds() {
			if bind.State && state != ToolStateHost {
				continue
			}
			if bind.State {
				bind.HostPath = t.stateBindHostPath(item.Name(), bind)
			}
			if seen[bind] {
				continue
			}
			seen[bind] = true
			binds = append(binds, bind)
		}
	}
	return binds
}

func (t *Toolset) stateBindHostPath(name string, bind Bind) string {
	root := t.toolStates.StateRootFor(name)
	statePath := bind.StatePath
	if statePath == "" && bind.Target.Base == PathHome {
		statePath = bind.Target.Path
	}
	if root == "" || statePath == "" {
		return bind.HostPath
	}
	return filepath.Join(root, filepath.FromSlash(statePath))
}

func (t *Toolset) HostStateToolNames() []string {
	var names []string
	if t == nil {
		return names
	}
	for _, item := range t.ordered {
		if item.Name() == DockerToolName || t.toolStates.StateFor(item.Name()) != ToolStateHost || !hasStateBind(item) {
			continue
		}
		names = append(names, item.Name())
	}
	return names
}

func hasStateBind(item Tool) bool {
	for _, bind := range item.Binds() {
		if bind.State {
			return true
		}
	}
	return false
}

func (t *Toolset) PathEntries() []PathTarget {
	var entries []PathTarget
	seen := map[PathTarget]bool{}
	for _, item := range t.ordered {
		for _, entry := range item.PathEntries() {
			if seen[entry] {
				continue
			}
			seen[entry] = true
			entries = append(entries, entry)
		}
	}
	return entries
}

func (t *Toolset) HostInit(ctx context.Context, opts *CommandOptions) error {
	for _, item := range t.ordered {
		if err := item.HostInit(ctx, opts); err != nil {
			return err
		}
	}
	return nil
}

func (t *Toolset) SandboxContextSetup(ctx *RunContext) error {
	for _, item := range t.ordered {
		if err := item.SandboxContextSetup(ctx); err != nil {
			return err
		}
	}
	return nil
}

func (t *Toolset) SandboxInit(ctx context.Context, run *RunContext) error {
	for _, item := range t.ordered {
		if err := item.SandboxInit(ctx, run); err != nil {
			return err
		}
	}
	return nil
}

func (t *Toolset) RegisterContextFiles(ctx context.Context, run *RunContext) error {
	for _, item := range t.ordered {
		registrar, ok := item.(ContextFileTool)
		if !ok {
			continue
		}
		if err := registrar.RegisterContextFiles(ctx, run); err != nil {
			return err
		}
	}
	return nil
}

func (t *Toolset) Install(ctx context.Context, run *RunContext) error {
	for _, item := range t.ordered {
		if err := item.Install(ctx, run); err != nil {
			return err
		}
	}
	return nil
}

func (t *Toolset) Upgrade(ctx context.Context, run *RunContext) error {
	for _, item := range t.ordered {
		if err := item.Upgrade(ctx, run); err != nil {
			return err
		}
	}
	return nil
}

func (t *Toolset) Launch(ctx context.Context, run *RunContext) error {
	if t.primary == nil {
		return fmt.Errorf("toolset cannot launch without a primary tool")
	}
	if run.Options.Install {
		return t.Install(ctx, run)
	}
	if run.Options.Upgrade {
		if err := t.Upgrade(ctx, run); err != nil {
			return err
		}
		return t.primary.Launch(ctx, run)
	}
	if err := t.Install(ctx, run); err != nil {
		return err
	}
	return t.primary.Launch(ctx, run)
}
