package configpatch

// Tests JSON and TOML patch application, intent compilation, and raw RFC 6902
// operations.

import (
	"encoding/json"
	"slices"
	"testing"

	"github.com/pelletier/go-toml/v2"
)

func TestApplyTOMLEnsureCreatesMissingPluginList(t *testing.T) {
	t.Parallel()

	patched, err := ApplyTOML(nil, pluginEnsurePatch())
	if err != nil {
		t.Fatal(err)
	}
	if got := enabledPlugins(t, patched); !slices.Equal(got, []string{"toby-session"}) {
		t.Fatalf("enabled = %#v", got)
	}
}

func TestApplyTOMLEnsureAppendsWithoutReplacingList(t *testing.T) {
	t.Parallel()

	patched, err := ApplyTOML([]byte(`
[ui]
max_thoughts_width = 120

[plugins]
enabled = ["already"]
`), pluginEnsurePatch())
	if err != nil {
		t.Fatal(err)
	}

	if got := enabledPlugins(t, patched); !slices.Equal(got, []string{"already", "toby-session"}) {
		t.Fatalf("enabled = %#v", got)
	}
	if got := maxThoughtsWidth(t, patched); got != 120 {
		t.Fatalf("max_thoughts_width = %d, want integer 120", got)
	}
}

func TestApplyTOMLEnsureIsIdempotent(t *testing.T) {
	t.Parallel()

	first, err := ApplyTOML([]byte(`
[plugins]
enabled = ["toby-session", "already"]
`), pluginEnsurePatch())
	if err != nil {
		t.Fatal(err)
	}
	second, err := ApplyTOML(first, pluginEnsurePatch())
	if err != nil {
		t.Fatal(err)
	}
	if got := enabledPlugins(t, second); !slices.Equal(got, []string{"toby-session", "already"}) {
		t.Fatalf("enabled = %#v", got)
	}
}

func TestApplyTOMLRemoveDropsMatchingArrayMember(t *testing.T) {
	t.Parallel()

	patched, err := ApplyTOML([]byte(`
[plugins]
enabled = ["keep", "old-plugin", "keep"]
`), Patch{
		Remove: []Value{{Path: "/plugins/enabled", Value: "old-plugin"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := enabledPlugins(t, patched); !slices.Equal(got, []string{"keep", "keep"}) {
		t.Fatalf("enabled = %#v", got)
	}
}

func TestApplyJSONOperationsThenEnsure(t *testing.T) {
	t.Parallel()

	patch := Patch{
		Operations: []Operation{
			{Op: "add", Path: "/plugins", Value: map[string]any{}},
			{Op: "add", Path: "/plugins/enabled", Value: []any{"seed"}},
		},
		Ensure: []Value{{Path: "/plugins/enabled", Value: "toby-session"}},
	}

	patched, err := ApplyJSON(nil, patch)
	if err != nil {
		t.Fatal(err)
	}
	var document struct {
		Plugins struct {
			Enabled []string `json:"enabled"`
		} `json:"plugins"`
	}
	if err := json.Unmarshal(patched, &document); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(document.Plugins.Enabled, []string{"seed", "toby-session"}) {
		t.Fatalf("enabled = %#v", document.Plugins.Enabled)
	}
}

func TestApplyJSONReplaceMoveCopyTestAndRemove(t *testing.T) {
	t.Parallel()

	patched, err := ApplyJSON([]byte(`{
  "keep": "yes",
  "name": "old",
  "source": 7
}`), Patch{Operations: []Operation{
		{Op: "test", Path: "/keep", Value: "yes"},
		{Op: "replace", Path: "/name", Value: "new"},
		{Op: "copy", From: "/name", Path: "/copied"},
		{Op: "move", From: "/source", Path: "/moved"},
		{Op: "remove", Path: "/keep"},
	}})
	if err != nil {
		t.Fatal(err)
	}

	var document map[string]any
	if err := json.Unmarshal(patched, &document); err != nil {
		t.Fatal(err)
	}
	if _, found := document["keep"]; found {
		t.Fatalf("keep still present: %#v", document)
	}
	if document["name"] != "new" || document["copied"] != "new" {
		t.Fatalf("name/copied = %#v", document)
	}
	if got, ok := document["moved"].(float64); !ok || got != 7 {
		t.Fatalf("moved = %#v", document["moved"])
	}
}

func TestApplyTOMLPreservesArrayOfTablesAndIntegers(t *testing.T) {
	t.Parallel()

	patched, err := ApplyTOML([]byte(`
[marketplace]
official = true

[[marketplace.sources]]
name = "official"
git = "https://example.invalid/plugins.git"

[ui]
max_thoughts_width = 120
`), pluginEnsurePatch())
	if err != nil {
		t.Fatal(err)
	}

	var document struct {
		Marketplace struct {
			Official bool `toml:"official"`
			Sources  []struct {
				Name string `toml:"name"`
				Git  string `toml:"git"`
			} `toml:"sources"`
		} `toml:"marketplace"`
		UI struct {
			MaxThoughtsWidth int `toml:"max_thoughts_width"`
		} `toml:"ui"`
	}
	if err := toml.Unmarshal(patched, &document); err != nil {
		t.Fatal(err)
	}
	if !document.Marketplace.Official ||
		len(document.Marketplace.Sources) != 1 ||
		document.Marketplace.Sources[0].Name != "official" ||
		document.UI.MaxThoughtsWidth != 120 {
		t.Fatalf("round-trip = %#v", document)
	}
	if got := enabledPlugins(t, patched); !slices.Equal(got, []string{"toby-session"}) {
		t.Fatalf("enabled = %#v", got)
	}
}

func TestApplyJSONRejectsNonObjectRoot(t *testing.T) {
	t.Parallel()

	if _, err := ApplyJSON([]byte(`[]`), pluginEnsurePatch()); err == nil {
		t.Fatal("array document was accepted")
	}
}

func TestPatchValidateRejectsConflictingIntent(t *testing.T) {
	t.Parallel()

	err := Patch{
		Ensure: []Value{{Path: "/plugins/enabled", Value: "toby-session"}},
		Remove: []Value{{Path: "/plugins/enabled", Value: "toby-session"}},
	}.Validate()
	if err == nil {
		t.Fatal("conflicting ensure/remove was accepted")
	}
}

func TestValidateRejectsUnknownOperation(t *testing.T) {
	t.Parallel()

	if err := (Patch{Operations: []Operation{{Op: "merge", Path: "/x"}}}).Validate(); err == nil {
		t.Fatal("unknown op was accepted")
	}
}

func TestPatchCloneDetachesIntentValues(t *testing.T) {
	t.Parallel()

	original := Patch{
		Ensure: []Value{{
			Path:  "/object",
			Value: map[string]any{"name": "toby-session"},
		}},
	}
	clone := original.Clone()
	original.Ensure[0].Value.(map[string]any)["name"] = "mutated"
	if clone.Ensure[0].Value.(map[string]any)["name"] != "toby-session" {
		t.Fatalf("clone aliases original value: %#v", clone.Ensure[0].Value)
	}
}

func pluginEnsurePatch() Patch {
	return Patch{
		Ensure: []Value{{Path: "/plugins/enabled", Value: "toby-session"}},
	}
}

func enabledPlugins(t *testing.T, data []byte) []string {
	t.Helper()
	var document struct {
		Plugins struct {
			Enabled []string `toml:"enabled"`
		} `toml:"plugins"`
	}
	if err := toml.Unmarshal(data, &document); err != nil {
		t.Fatal(err)
	}
	return document.Plugins.Enabled
}

func maxThoughtsWidth(t *testing.T, data []byte) int {
	t.Helper()
	var document struct {
		UI struct {
			MaxThoughtsWidth int `toml:"max_thoughts_width"`
		} `toml:"ui"`
	}
	if err := toml.Unmarshal(data, &document); err != nil {
		t.Fatal(err)
	}
	return document.UI.MaxThoughtsWidth
}
