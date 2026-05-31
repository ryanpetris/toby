package warning

import (
	"bytes"
	"strings"
	"testing"
)

func TestParseSuppression(t *testing.T) {
	all, err := ParseSuppression(true, "warnings")
	if err != nil {
		t.Fatal(err)
	}
	if !all.Set || !all.All || !all.Suppresses(ToolHostState) {
		t.Fatalf("all suppression = %#v", all)
	}

	ids, err := ParseSuppression([]any{" tool.host-state ", "project.missing"}, "warnings")
	if err != nil {
		t.Fatal(err)
	}
	if !ids.Set || ids.All || !ids.Suppresses(ToolHostState) || !ids.Suppresses(ProjectMissing) || ids.Suppresses(OpenCodeModelDiscovery) {
		t.Fatalf("id suppression = %#v", ids)
	}

	stringsIDs, err := ParseSuppression([]string{"project.duplicate"}, "warnings")
	if err != nil {
		t.Fatal(err)
	}
	if !stringsIDs.Suppresses(ProjectDuplicate) {
		t.Fatalf("string id suppression = %#v", stringsIDs)
	}
}

func TestParseSuppressionRejectsInvalidValues(t *testing.T) {
	tests := []struct {
		name string
		raw  any
		want string
	}{
		{name: "wrong type", raw: "no", want: "warnings must be a boolean or array of strings"},
		{name: "item not string", raw: []any{1}, want: "warnings[0] must be a string"},
		{name: "unknown id", raw: []any{"unknown.warning"}, want: "warnings[0]: warning id must be one of"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := ParseSuppression(tt.raw, "warnings")
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("err = %v, want containing %q", err, tt.want)
			}
		})
	}
}

func TestSuppressionCloneAndMerge(t *testing.T) {
	src := Suppression{Set: true, IDs: map[ID]bool{ToolHostState: true}}
	clone := src.Clone()
	clone.IDs[ToolHostState] = false
	if !src.Suppresses(ToolHostState) {
		t.Fatalf("source changed after clone mutation: %#v", src)
	}

	dst := Suppression{Set: true, All: true}
	dst.Merge(Suppression{})
	if !dst.All {
		t.Fatalf("unset merge changed suppression: %#v", dst)
	}
	dst.Merge(src)
	src.IDs[ToolHostState] = false
	if dst.All || !dst.Suppresses(ToolHostState) {
		t.Fatalf("merge did not clone source: %#v", dst)
	}
}

func TestFprintfHonorsSuppression(t *testing.T) {
	var buf bytes.Buffer
	Fprintf(&buf, Suppression{All: true}, ToolHostState, "hidden %s", "warning")
	if buf.Len() != 0 {
		t.Fatalf("suppressed warning wrote %q", buf.String())
	}

	Fprintf(&buf, Suppression{}, ToolHostState, "visible %s", "warning")
	if got := buf.String(); !strings.Contains(got, "toby: warning[tool.host-state]: visible warning\n") {
		t.Fatalf("warning output = %q", got)
	}
}

func TestParseIDTrimsAndRejectsUnknownIDs(t *testing.T) {
	if id, err := ParseID(" project.autoload-disabled "); err != nil || id != ProjectAutoloadDisabled {
		t.Fatalf("ParseID = %q, %v", id, err)
	}
	if _, err := ParseID("unknown"); err == nil {
		t.Fatal("expected unknown id to fail")
	}
}
