package configfile

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

type sample struct {
	Name string `json:"name" yaml:"name"`
}

func TestDecodeStrict(t *testing.T) {
	for _, tt := range []struct {
		name   string
		data   string
		format Format
	}{
		{"json", `{"name":"demo"}`, FormatJSON},
		{"yaml", "name: demo\n", FormatYAML},
	} {
		t.Run(tt.name, func(t *testing.T) {
			var got sample
			if err := Decode([]byte(tt.data), tt.format, "test", &got); err != nil {
				t.Fatal(err)
			}
			if got.Name != "demo" {
				t.Fatalf("name = %q, want demo", got.Name)
			}
		})
	}
}

func TestDecodeRejectsUnknownFields(t *testing.T) {
	for _, tt := range []struct {
		name   string
		data   string
		format Format
	}{
		{"json", `{"name":"demo","extra":true}`, FormatJSON},
		{"yaml", "name: demo\nextra: true\n", FormatYAML},
	} {
		t.Run(tt.name, func(t *testing.T) {
			var got sample
			err := Decode([]byte(tt.data), tt.format, "test", &got)
			if err == nil || !strings.Contains(err.Error(), "extra") {
				t.Fatalf("err = %v, want unknown-field error mentioning extra", err)
			}
		})
	}
}

func TestDecodeRejectsTrailingJSON(t *testing.T) {
	var got sample
	err := Decode([]byte(`{"name":"a"} {}`), FormatJSON, "test", &got)
	if err == nil || !strings.Contains(err.Error(), "multiple JSON values") {
		t.Fatalf("err = %v, want multiple JSON values", err)
	}
}

func TestDecodeEmptyIsNoop(t *testing.T) {
	for _, tt := range []struct {
		name   string
		data   string
		format Format
	}{
		{"json", "   \n", FormatJSON},
		{"yaml", "", FormatYAML},
	} {
		t.Run(tt.name, func(t *testing.T) {
			got := sample{Name: "unchanged"}
			if err := Decode([]byte(tt.data), tt.format, "test", &got); err != nil {
				t.Fatal(err)
			}
			if got.Name != "unchanged" {
				t.Fatalf("empty input mutated dest: %q", got.Name)
			}
		})
	}
}

func TestDecodeFileInfersFormat(t *testing.T) {
	dir := t.TempDir()
	jsonPath := filepath.Join(dir, "config.json")
	yamlPath := filepath.Join(dir, "config.yml")
	if err := os.WriteFile(jsonPath, []byte(`{"name":"j"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(yamlPath, []byte("name: y\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	var fromJSON, fromYAML sample
	if err := DecodeFile(jsonPath, &fromJSON); err != nil {
		t.Fatal(err)
	}
	if err := DecodeFile(yamlPath, &fromYAML); err != nil {
		t.Fatal(err)
	}
	if fromJSON.Name != "j" || fromYAML.Name != "y" {
		t.Fatalf("json=%q yaml=%q", fromJSON.Name, fromYAML.Name)
	}

	var ignored sample
	if err := DecodeFile(filepath.Join(dir, "config.toml"), &ignored); err == nil || !strings.Contains(err.Error(), "unsupported") {
		t.Fatalf("err = %v, want unsupported extension", err)
	}
}

func TestDecodeIntoStrictMapToStruct(t *testing.T) {
	var got sample
	if err := DecodeInto(map[string]any{"name": "demo"}, &got); err != nil {
		t.Fatal(err)
	}
	if got.Name != "demo" {
		t.Fatalf("name = %q, want demo", got.Name)
	}

	// Unknown keys are rejected, matching the file decoders.
	err := DecodeInto(map[string]any{"name": "demo", "extra": true}, &sample{})
	if err == nil || !strings.Contains(err.Error(), "extra") {
		t.Fatalf("err = %v, want unknown-field error mentioning extra", err)
	}

	// Empty map is a no-op.
	keep := sample{Name: "unchanged"}
	if err := DecodeInto(nil, &keep); err != nil {
		t.Fatal(err)
	}
	if keep.Name != "unchanged" {
		t.Fatalf("empty map mutated dest: %q", keep.Name)
	}
}

func TestMergeDeepMergesAndClones(t *testing.T) {
	srcReplace := map[string]any{"key": "value"}
	dst := map[string]any{
		"nested":  map[string]any{"base": "keep"},
		"replace": []any{"old"},
	}
	src := map[string]any{
		"nested":  map[string]any{"extra": "add"},
		"replace": []any{srcReplace},
	}

	Merge(dst, src)
	srcReplace["key"] = "mutated"

	if got, want := dst["nested"], map[string]any{"base": "keep", "extra": "add"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("nested = %#v, want %#v", got, want)
	}
	if got, want := dst["replace"], []any{map[string]any{"key": "value"}}; !reflect.DeepEqual(got, want) {
		t.Fatalf("replace = %#v, want %#v (list replaced, value cloned)", got, want)
	}
}
