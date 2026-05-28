package tool

import (
	"context"
	"fmt"
	"sort"
	"strings"

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
	builder := toolBuilder{registry: r}
	ordered, err := builder.resolve(requested)
	if err != nil {
		return nil, err
	}
	var primaryTool Tool
	if primary != "" {
		var ok bool
		primaryTool, ok = r.Get(primary)
		if !ok {
			return nil, fmt.Errorf("unknown primary tool: %s", primary)
		}
	}
	return &Toolset{primary: primaryTool, ordered: ordered}, nil
}

type toolBuilder struct {
	registry      *Registry
	orderedNames  []string
	visitingTools []string
	visitingNames map[string]bool
}

func (b *toolBuilder) resolve(requested []string) ([]Tool, error) {
	b.visitingNames = map[string]bool{}
	for _, name := range requested {
		if _, ok := b.registry.Get(name); !ok {
			return nil, fmt.Errorf("unknown tool: %s", name)
		}
		if err := b.add(name); err != nil {
			return nil, err
		}
	}
	orderedNames := append([]string(nil), b.orderedNames...)
	for i, j := 0, len(orderedNames)-1; i < j; i, j = i+1, j-1 {
		orderedNames[i], orderedNames[j] = orderedNames[j], orderedNames[i]
	}
	ordered := make([]Tool, 0, len(orderedNames))
	for _, name := range orderedNames {
		ordered = append(ordered, b.registry.MustGet(name))
	}
	return ordered, nil
}

func (b *toolBuilder) add(name string) error {
	if b.visitingNames[name] {
		start := 0
		for i, item := range b.visitingTools {
			if item == name {
				start = i
				break
			}
		}
		cycle := append(append([]string(nil), b.visitingTools[start:]...), name)
		return fmt.Errorf("tool dependency cycle: %s", strings.Join(cycle, " -> "))
	}
	for i, item := range b.orderedNames {
		if item == name {
			b.orderedNames = append(b.orderedNames[:i], b.orderedNames[i+1:]...)
			break
		}
	}
	b.orderedNames = append(b.orderedNames, name)
	b.visitingTools = append(b.visitingTools, name)
	b.visitingNames[name] = true

	item := b.registry.MustGet(name)
	for _, dep := range item.Dependencies() {
		if _, ok := b.registry.Get(dep); !ok {
			return fmt.Errorf("tool %s depends on unknown tool: %s", name, dep)
		}
		if err := b.add(dep); err != nil {
			return err
		}
	}

	b.visitingTools = b.visitingTools[:len(b.visitingTools)-1]
	delete(b.visitingNames, name)
	return nil
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
	primary Tool
	ordered []Tool
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
	for _, item := range t.ordered {
		binds = append(binds, item.Binds()...)
	}
	return binds
}

func (t *Toolset) PathEntries() []string {
	var entries []string
	for _, item := range t.ordered {
		entries = append(entries, item.PathEntries()...)
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

func (t *Toolset) Install(ctx context.Context, run *RunContext, force bool) error {
	for _, item := range t.ordered {
		if err := item.Install(ctx, run, force); err != nil {
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
		return t.Install(ctx, run, true)
	}
	if err := t.Install(ctx, run, run.Options.Upgrade); err != nil {
		return err
	}
	return t.primary.Launch(ctx, run)
}
