package tools

// Dependency ordering: a deterministic topological sort that places every tool
// after the tools it depends on. This is the sole source of launch order — tools
// carry no priority number; "run after X" is expressed by depending on X.

import (
	"fmt"
	"sort"
	"strings"
)

// topologicalOrder returns items ordered so each tool follows all of its
// dependencies, choosing the alphabetically-smallest ready tool at each step for
// a deterministic result. Dependency names not present in items are ignored, so
// callers must pass a closed set (or validate existence separately). It returns
// an error naming the offending tools if the dependency graph has a cycle.
func topologicalOrder(items []Tool) ([]Tool, error) {
	byName := make(map[string]Tool, len(items))
	for _, item := range items {
		byName[item.Name()] = item
	}

	emitted := make(map[string]bool, len(items))
	result := make([]Tool, 0, len(items))
	for len(result) < len(items) {
		next := ""
		for _, item := range items {
			name := item.Name()
			if emitted[name] || !dependenciesEmitted(item, byName, emitted) {
				continue
			}
			if next == "" || name < next {
				next = name
			}
		}
		if next == "" {
			return nil, fmt.Errorf("tool dependency cycle among: %s", strings.Join(unemitted(items, emitted), ", "))
		}
		emitted[next] = true
		result = append(result, byName[next])
	}
	return result, nil
}

// dependenciesEmitted reports whether every in-set dependency of item has already
// been emitted.
func dependenciesEmitted(item Tool, byName map[string]Tool, emitted map[string]bool) bool {
	for _, dep := range item.Dependencies() {
		if _, inSet := byName[dep]; inSet && !emitted[dep] {
			return false
		}
	}
	return true
}

// unemitted returns the sorted names of items not yet emitted (the tools tangled
// in a dependency cycle).
func unemitted(items []Tool, emitted map[string]bool) []string {
	var names []string
	for _, item := range items {
		if !emitted[item.Name()] {
			names = append(names, item.Name())
		}
	}
	sort.Strings(names)
	return names
}
