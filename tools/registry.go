package tools

// The Registry indexes the registered tools, validates their dependency ordering,
// assembles the group catalog, and builds ordered Toolsets from a selection.

import (
	"fmt"
	"sort"
)

// GroupOrder is the order tool groups are listed and expanded in.
var GroupOrder = []string{GroupAI, GroupUI, GroupSystem, GroupVCS, GroupCommand}

// Registry indexes the registered tools by name and validates their dependency
// ordering. The group catalog (byGroup) is assembled from each tool's primary
// Group; there is no central group→tools map.
type Registry struct {
	tools   map[string]Tool
	byGroup map[string][]string
}

// NewRegistry indexes the tools supplied via the fx "tools" group, rejecting
// empty or duplicate names and validating that every dependency exists and that
// the dependency graph is acyclic. It also assembles the group catalog from each
// tool's primary Group.
func NewRegistry(toolList []Tool) (*Registry, error) {
	registry := &Registry{tools: make(map[string]Tool, len(toolList)), byGroup: map[string][]string{}}
	for _, item := range toolList {
		if item.Name() == "" {
			return nil, fmt.Errorf("registered tool must define a non-empty name")
		}
		if _, exists := registry.tools[item.Name()]; exists {
			return nil, fmt.Errorf("duplicate tool registration: %s", item.Name())
		}
		registry.tools[item.Name()] = item
		if group := item.Group(); group != "" {
			registry.byGroup[group] = append(registry.byGroup[group], item.Name())
		}
	}
	for _, names := range registry.byGroup {
		sort.Strings(names)
	}
	for _, item := range toolList {
		for _, dep := range item.Dependencies() {
			if _, ok := registry.tools[dep]; !ok {
				return nil, fmt.Errorf("tool %s depends on unknown tool: %s", item.Name(), dep)
			}
		}
	}
	if _, err := topologicalOrder(toolList); err != nil {
		return nil, err
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

// LaunchTools returns the registered, launchable tools in group order.
func (r *Registry) LaunchTools() []Tool {
	seen := map[string]bool{}
	result := []Tool{}
	for _, group := range GroupOrder {
		for _, name := range r.byGroup[group] {
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

// Build resolves the requested tools (and the primary) plus their transitive
// dependencies into a Toolset ordered topologically: every tool follows the tools
// it depends on.
func (r *Registry) Build(requested []string, primary string) (*Toolset, error) {
	closure := map[string]Tool{}
	var gather func(string) error
	gather = func(name string) error {
		if _, ok := closure[name]; ok {
			return nil
		}
		item, ok := r.Get(name)
		if !ok {
			return fmt.Errorf("unknown tool: %s", name)
		}
		closure[name] = item
		for _, dep := range item.Dependencies() {
			if err := gather(dep); err != nil {
				return err
			}
		}
		return nil
	}

	for _, name := range requested {
		if err := gather(name); err != nil {
			return nil, err
		}
	}

	var primaryTool Tool
	if primary != "" {
		var ok bool
		primaryTool, ok = r.Get(primary)
		if !ok {
			return nil, fmt.Errorf("unknown primary tool: %s", primary)
		}
		if err := gather(primary); err != nil {
			return nil, err
		}
	}

	items := make([]Tool, 0, len(closure))
	for _, item := range closure {
		items = append(items, item)
	}

	ordered, err := topologicalOrder(items)
	if err != nil {
		return nil, err
	}
	return &Toolset{primary: primaryTool, ordered: ordered}, nil
}

// ExpandGroups expands the named groups into their member tool names (the tools
// filed under each group), in group order and without duplicates.
func (r *Registry) ExpandGroups(groups []string) []string {
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
		for _, name := range r.byGroup[group] {
			if seen[name] {
				continue
			}
			seen[name] = true
			result = append(result, name)
		}
	}
	return result
}

// Toolset is an ordered selection of tools for one launch, with a designated
// primary. The lifecycle runner drives it through the phases.
type Toolset struct {
	primary Tool
	ordered []Tool
}

// OrderedTools returns the selected tools in lifecycle order.
func (t *Toolset) OrderedTools() []Tool {
	return append([]Tool(nil), t.ordered...)
}

// OrderedToolNames returns the selected tool names in lifecycle order.
func (t *Toolset) OrderedToolNames() []string {
	names := make([]string, len(t.ordered))
	for i, item := range t.ordered {
		names[i] = item.Name()
	}
	return names
}

// Primary returns the foreground tool the session launches, or nil.
func (t *Toolset) Primary() Tool {
	if t == nil {
		return nil
	}
	return t.primary
}

// Has reports whether the named tool is in the set.
func (t *Toolset) Has(name string) bool {
	for _, item := range t.ordered {
		if item.Name() == name {
			return true
		}
	}
	return false
}
