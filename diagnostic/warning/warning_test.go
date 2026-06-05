package warning

import (
	"bytes"
	"strings"
	"testing"
)

func TestSuppressionFromList(t *testing.T) {
	all, err := SuppressionFromList([]string{"*"}, "warnings")
	if err != nil {
		t.Fatal(err)
	}
	if !all.Set || !all.All || !all.Suppresses(MountHostBacking) {
		t.Fatalf("all suppression = %#v", all)
	}

	ids, err := SuppressionFromList([]string{" mount.host-backing ", "project.missing"}, "warnings")
	if err != nil {
		t.Fatal(err)
	}
	if !ids.Set || ids.All || !ids.Suppresses(MountHostBacking) || !ids.Suppresses(ProjectMissing) || ids.Suppresses(OpenCodeModelDiscovery) {
		t.Fatalf("id suppression = %#v", ids)
	}

	empty, err := SuppressionFromList(nil, "warnings")
	if err != nil {
		t.Fatal(err)
	}
	if !empty.Set || empty.All || empty.Suppresses(MountHostBacking) {
		t.Fatalf("empty suppression = %#v", empty)
	}
}

func TestSuppressionFromListRejectsUnknownIDs(t *testing.T) {
	_, err := SuppressionFromList([]string{"unknown.warning"}, "warnings")
	if err == nil || !strings.Contains(err.Error(), "warnings[0]: warning id must be one of") {
		t.Fatalf("err = %v, want unknown-id error", err)
	}
}

func TestSuppressionCloneAndMerge(t *testing.T) {
	src := Suppression{Set: true, IDs: map[ID]bool{MountHostBacking: true}}
	clone := src.Clone()
	clone.IDs[MountHostBacking] = false
	if !src.Suppresses(MountHostBacking) {
		t.Fatalf("source changed after clone mutation: %#v", src)
	}

	dst := Suppression{Set: true, All: true}
	dst.Merge(Suppression{})
	if !dst.All {
		t.Fatalf("unset merge changed suppression: %#v", dst)
	}
	dst.Merge(src)
	src.IDs[MountHostBacking] = false
	if dst.All || !dst.Suppresses(MountHostBacking) {
		t.Fatalf("merge did not clone source: %#v", dst)
	}
}

func TestFprintfHonorsSuppression(t *testing.T) {
	var buf bytes.Buffer
	Fprintf(&buf, Suppression{All: true}, MountHostBacking, "hidden %s", "warning")
	if buf.Len() != 0 {
		t.Fatalf("suppressed warning wrote %q", buf.String())
	}

	Fprintf(&buf, Suppression{}, MountHostBacking, "visible %s", "warning")
	if got := buf.String(); !strings.Contains(got, "toby: warning[mount.host-backing]: visible warning\n") {
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
