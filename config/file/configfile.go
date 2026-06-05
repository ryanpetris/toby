// Package configfile decodes configuration files: strict JSON/YAML decoding into
// typed structs (rejecting unknown fields), plus deep-merging, cloning, and
// number normalization of the generic maps used for passthrough config.
package configfile

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"gopkg.in/yaml.v3"
)

type Format int

const (
	FormatJSON Format = iota
	FormatYAML
)

// Decode parses data in the given format into dest. Decoding is strict: unknown
// fields are rejected. Empty (or whitespace-only) input leaves dest unchanged.
func Decode(data []byte, format Format, label string, dest any) error {
	if format == FormatYAML {
		return decodeYAML(data, label, dest)
	}
	return decodeJSON(data, label, dest)
}

// DecodeInto strict-decodes an already-decoded generic map into dest, rejecting
// unknown fields. It is the map→struct counterpart of Decode, used after merging
// several source maps with Merge: re-marshal the merged map and feed it through
// the same strict JSON decoder. Open passthrough fields keep their map[string]any
// type, so strict decoding never recurses into them.
func DecodeInto(src map[string]any, dest any) error {
	if len(src) == 0 {
		return nil
	}
	data, err := json.Marshal(src)
	if err != nil {
		return err
	}
	return decodeJSON(data, "config", dest)
}

// DecodeFile reads path and decodes it into dest, inferring the format from the
// file extension: .json → JSON, .yaml/.yml → YAML. Other extensions are an error.
// An empty file leaves dest unchanged.
func DecodeFile(path string, dest any) error {
	format, err := formatForPath(path)
	if err != nil {
		return err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	return Decode(data, format, path, dest)
}

func formatForPath(path string) (Format, error) {
	switch ext := filepath.Ext(path); ext {
	case ".json":
		return FormatJSON, nil
	case ".yaml", ".yml":
		return FormatYAML, nil
	default:
		return 0, fmt.Errorf("unsupported config file extension %q", ext)
	}
}

// Decode errors are returned as-is so callers add the file/source context: a
// strict decode surfaces both syntax errors and semantic ones (unknown fields,
// custom unmarshalers), and double-prefixing them reads poorly.
func decodeJSON(data []byte, label string, dest any) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(dest); err != nil {
		if errors.Is(err, io.EOF) {
			return nil
		}
		return err
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		if err != nil {
			return err
		}
		return fmt.Errorf("multiple JSON values in %s", label)
	}
	return nil
}

func decodeYAML(data []byte, _ string, dest any) error {
	decoder := yaml.NewDecoder(bytes.NewReader(data))
	decoder.KnownFields(true)
	if err := decoder.Decode(dest); err != nil {
		if errors.Is(err, io.EOF) {
			return nil
		}
		return err
	}
	return nil
}

// Merge deep-merges src into dst: nested maps are merged recursively, and every
// other value (scalars, slices) replaces the destination. Merged-in values are
// cloned so dst never shares mutable structure with src.
func Merge(dst, src map[string]any) {
	for key, value := range src {
		srcMap, srcOK := value.(map[string]any)
		dstMap, dstOK := dst[key].(map[string]any)
		if srcOK && dstOK {
			Merge(dstMap, srcMap)
			continue
		}
		dst[key] = Clone(value)
	}
}

// NormalizeNumbers recursively converts json.Number values in a decoded value to
// int64 (when integral) or float64. Strict map→struct decoding (DecodeInto) leaves
// passthrough map[string]any fields holding json.Number; normalizing keeps those
// values rendering as plain numbers downstream regardless of the source format.
func NormalizeNumbers(value any) any {
	switch v := value.(type) {
	case map[string]any:
		for key, item := range v {
			v[key] = NormalizeNumbers(item)
		}
		return v
	case []any:
		for i, item := range v {
			v[i] = NormalizeNumbers(item)
		}
		return v
	case json.Number:
		if i, err := v.Int64(); err == nil {
			return i
		}
		if f, err := v.Float64(); err == nil {
			return f
		}
		return v.String()
	default:
		return value
	}
}

func CloneMap(src map[string]any) map[string]any {
	if src == nil {
		return map[string]any{}
	}
	clone := make(map[string]any, len(src))
	for key, value := range src {
		clone[key] = Clone(value)
	}
	return clone
}

func Clone(value any) any {
	switch v := value.(type) {
	case map[string]any:
		return CloneMap(v)
	case []any:
		clone := make([]any, len(v))
		for i, item := range v {
			clone[i] = Clone(item)
		}
		return clone
	default:
		return value
	}
}
