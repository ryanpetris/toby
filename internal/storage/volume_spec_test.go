package storage

// Covers volume-spec defaults and metadata-filter validation.

import "testing"

func TestVolumeSpecNormalizeAppliesDefaults(t *testing.T) {
	home, err := (VolumeSpec{
		Type: VolumeTypeHome,
		Name: "workspace",
	}).Normalize()
	if err != nil {
		t.Fatal(err)
	}
	if home.Profile != defaultProfile {
		t.Fatalf("home profile = %q", home.Profile)
	}

	tool, err := (VolumeSpec{
		Type:    VolumeTypeTool,
		Name:    "opencode",
		Purpose: "data",
	}).Normalize()
	if err != nil {
		t.Fatal(err)
	}
	if tool.Profile != defaultProfile {
		t.Fatalf("tool profile = %q", tool.Profile)
	}
}

func TestVolumeSpecNormalizeRejectsIncompleteShapes(t *testing.T) {
	tests := []VolumeSpec{
		{},
		{Type: VolumeTypeHome},
		{Type: VolumeTypeHome, Name: "home", Purpose: "data"},
		{Type: VolumeTypeTool, Name: "opencode"},
		{Type: "unknown", Name: "name"},
	}
	for _, spec := range tests {
		if _, err := spec.Normalize(); err == nil {
			t.Fatalf("invalid specification was accepted: %#v", spec)
		}
	}
}

func TestVolumeFilterNormalizeSupportsPartialMatches(t *testing.T) {
	filter, err := (VolumeFilter{
		Type:    VolumeTypeTool,
		Profile: " work ",
	}).Normalize()
	if err != nil {
		t.Fatal(err)
	}
	if filter.Profile != "work" || filter.Name != "" {
		t.Fatalf("filter = %#v", filter)
	}

	if _, err := (VolumeFilter{
		Type:    VolumeTypeHome,
		Purpose: "data",
	}).Normalize(); err == nil {
		t.Fatal("accepted a home purpose filter")
	}
	for _, filter := range []VolumeFilter{
		{Type: " "},
		{Profile: " "},
	} {
		if _, err := filter.Normalize(); err == nil {
			t.Fatalf("accepted a blank filter: %#v", filter)
		}
	}
}
