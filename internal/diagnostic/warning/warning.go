package warning

import (
	"fmt"
	"io"
	"os"
	"strings"
)

type ID string

const (
	MountHostBacking        ID = "mount.host-backing"
	OpenCodeModelDiscovery  ID = "opencode.model-discovery"
	ProjectAutoloadDisabled ID = "project.autoload-disabled"
	ProjectDuplicate        ID = "project.duplicate"
	ProjectMissing          ID = "project.missing"
)

type Suppression struct {
	Set bool
	All bool
	IDs map[ID]bool
}

func ParseSuppression(raw any, label string) (Suppression, error) {
	switch value := raw.(type) {
	case bool:
		return Suppression{Set: true, All: value}, nil
	case []any:
		return parseIDList(value, label)
	case []string:
		items := make([]any, 0, len(value))
		for _, item := range value {
			items = append(items, item)
		}
		return parseIDList(items, label)
	default:
		return Suppression{}, fmt.Errorf("%s must be a boolean or array of strings", label)
	}
}

func (s Suppression) Clone() Suppression {
	clone := Suppression{Set: s.Set, All: s.All}
	if len(s.IDs) > 0 {
		clone.IDs = make(map[ID]bool, len(s.IDs))
		for id, suppressed := range s.IDs {
			clone.IDs[id] = suppressed
		}
	}
	return clone
}

func (s *Suppression) Merge(src Suppression) {
	if !src.Set {
		return
	}
	*s = src.Clone()
}

func (s Suppression) Suppresses(id ID) bool {
	return s.All || s.IDs[id]
}

func Fprintf(stderr io.Writer, suppression Suppression, id ID, format string, args ...any) {
	if suppression.Suppresses(id) {
		return
	}
	if stderr == nil {
		stderr = os.Stderr
	}
	args = append([]any{id}, args...)
	_, _ = fmt.Fprintf(stderr, "toby: warning[%s]: "+format+"\n", args...)
}

func parseIDList(items []any, label string) (Suppression, error) {
	ids := map[ID]bool{}
	for i, item := range items {
		value, ok := item.(string)
		if !ok {
			return Suppression{}, fmt.Errorf("%s[%d] must be a string", label, i)
		}
		id, err := ParseID(value)
		if err != nil {
			return Suppression{}, fmt.Errorf("%s[%d]: %w", label, i, err)
		}
		ids[id] = true
	}
	return Suppression{Set: true, IDs: ids}, nil
}

func ParseID(value string) (ID, error) {
	switch id := ID(strings.TrimSpace(value)); id {
	case MountHostBacking, OpenCodeModelDiscovery, ProjectAutoloadDisabled, ProjectDuplicate, ProjectMissing:
		return id, nil
	default:
		return "", fmt.Errorf("warning id must be one of %q, %q, %q, %q, or %q", MountHostBacking, OpenCodeModelDiscovery, ProjectAutoloadDisabled, ProjectDuplicate, ProjectMissing)
	}
}
