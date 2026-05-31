package configfile

import (
	"reflect"
	"strings"
	"testing"
)

func TestDecodeJSONCStripsCommentsAndTrailingCommas(t *testing.T) {
	data := []byte(`{
		// Comment before a key.
		"url": "https://example.com//not-a-comment",
		"literal": "keep /* this */ and // this",
		"items": ["one", "two",],
		"nested": {
			"enabled": true,
		},
		/* multiline
		   comment preserves line structure */
	}`)

	decoded, err := Decode(data, FormatJSON, "test config")
	if err != nil {
		t.Fatal(err)
	}
	if decoded["url"] != "https://example.com//not-a-comment" || decoded["literal"] != "keep /* this */ and // this" {
		t.Fatalf("decoded strings = %#v", decoded)
	}
	if got, want := decoded["items"], []any{"one", "two"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("items = %#v, want %#v", got, want)
	}
	nested := decoded["nested"].(map[string]any)
	if nested["enabled"] != true {
		t.Fatalf("nested = %#v", nested)
	}
}

func TestDecodeJSONCRejectsMalformedInput(t *testing.T) {
	tests := []struct {
		name string
		data string
		want string
	}{
		{name: "unterminated block comment", data: `{"ok": true, /* nope`, want: "unterminated block comment"},
		{name: "unterminated string", data: `{"ok": "nope}`, want: "unterminated string"},
		{name: "multiple values", data: `{} {}`, want: "multiple JSON values"},
		{name: "not object", data: `[]`, want: "must be a JSON object"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := Decode([]byte(tt.data), FormatJSON, "test config")
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("err = %v, want containing %q", err, tt.want)
			}
		})
	}
}

func TestDecodeYAMLRejectsNonObjectAndNormalizesNestedValues(t *testing.T) {
	decoded, err := Decode([]byte("nested:\n  items:\n    - one\n"), FormatYAML, "test config")
	if err != nil {
		t.Fatal(err)
	}
	nested := decoded["nested"].(map[string]any)
	if got, want := nested["items"], []any{"one"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("nested items = %#v, want %#v", got, want)
	}

	_, err = Decode([]byte("- one\n"), FormatYAML, "test config")
	if err == nil || !strings.Contains(err.Error(), "must be a YAML object") {
		t.Fatalf("err = %v", err)
	}
}

func TestMergeDeepMergesDedupeListsAndClonesValues(t *testing.T) {
	srcReplace := map[string]any{"key": "value"}
	dst := map[string]any{
		"nested":       map[string]any{"base": "keep"},
		"instructions": []any{"base.md", "shared.md"},
		"plugin":       []any{"npm:@scope/pkg@1.0.0", "npm:plain@1.0.0", "literal"},
		"replace":      []any{"old"},
	}
	src := map[string]any{
		"nested":       map[string]any{"extra": "add"},
		"instructions": []any{"shared.md", "extra.md"},
		"plugin":       []any{"npm:@scope/pkg@2.0.0", "npm:plain@2.0.0", "literal"},
		"replace":      []any{srcReplace},
	}

	Merge(dst, src)
	srcReplace["key"] = "mutated"

	if got, want := dst["nested"], map[string]any{"base": "keep", "extra": "add"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("nested = %#v, want %#v", got, want)
	}
	if got, want := dst["instructions"], []any{"base.md", "shared.md", "extra.md"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("instructions = %#v, want %#v", got, want)
	}
	if got, want := dst["plugin"], []any{"npm:@scope/pkg@2.0.0", "npm:plain@2.0.0", "literal"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("plugin = %#v, want %#v", got, want)
	}
	replaced := dst["replace"].([]any)[0].(map[string]any)
	if replaced["key"] != "value" {
		t.Fatalf("replace was not cloned: %#v", replaced)
	}
}

func TestCanonicalPluginName(t *testing.T) {
	tests := map[string]string{
		"npm:@scope/pkg@1.2.3": "npm:@scope/pkg",
		"@scope/pkg@1.2.3":     "@scope/pkg",
		"npm:plain@1.2.3":      "npm:plain",
		"plain@1.2.3":          "plain",
		"@scope/pkg":           "@scope/pkg",
		"npm:":                 "npm:",
	}
	for specifier, want := range tests {
		if got := canonicalPluginName(specifier); got != want {
			t.Fatalf("canonicalPluginName(%q) = %q, want %q", specifier, got, want)
		}
	}
}
