package configpatch

// Applies a patch to a JSON or TOML object document.

import (
	"bytes"
	"encoding/json"
	"fmt"

	jsonpatch "github.com/evanphx/json-patch/v5"
	"github.com/pelletier/go-toml/v2"
)

// ApplyJSON applies the patch to a JSON object document. An empty document is
// treated as {}.
func ApplyJSON(document []byte, patch Patch) ([]byte, error) {
	object, err := decodeJSONDocument(document)
	if err != nil {
		return nil, err
	}
	object, err = apply(object, patch)
	if err != nil {
		return nil, err
	}
	return marshalJSON(object)
}

// ApplyTOML applies the patch to a TOML table document. An empty document is
// treated as an empty table. Integers stay integers across the round-trip.
func ApplyTOML(document []byte, patch Patch) ([]byte, error) {
	object, err := decodeTOMLDocument(document)
	if err != nil {
		return nil, err
	}
	object, err = apply(object, patch)
	if err != nil {
		return nil, err
	}
	data, err := toml.Marshal(object)
	if err != nil {
		return nil, fmt.Errorf("encode TOML: %w", err)
	}
	return data, nil
}

func apply(document map[string]any, patch Patch) (map[string]any, error) {
	if err := patch.Validate(); err != nil {
		return nil, err
	}

	current := document
	if len(patch.Operations) > 0 {
		var err error
		current, err = applyOperations(current, patch.Operations)
		if err != nil {
			return nil, err
		}
	}

	ops, err := compileIntents(current, patch)
	if err != nil {
		return nil, err
	}
	return applyOperations(current, ops)
}

func applyOperations(document map[string]any, ops []Operation) (map[string]any, error) {
	if len(ops) == 0 {
		return document, nil
	}

	encoded, err := marshalOperations(ops)
	if err != nil {
		return nil, err
	}
	decoded, err := jsonpatch.DecodePatch(encoded)
	if err != nil {
		return nil, fmt.Errorf("decode json patch: %w", err)
	}

	original, err := json.Marshal(document)
	if err != nil {
		return nil, fmt.Errorf("encode document: %w", err)
	}
	modified, err := decoded.ApplyWithOptions(original, applyOptions())
	if err != nil {
		return nil, fmt.Errorf("apply json patch: %w", err)
	}
	object, err := decodeObject(modified)
	if err != nil {
		return nil, fmt.Errorf("decode patched document: %w", err)
	}
	return object, nil
}

func applyOptions() *jsonpatch.ApplyOptions {
	options := jsonpatch.NewApplyOptions()
	options.EnsurePathExistsOnAdd = false
	options.AllowMissingPathOnRemove = false
	options.SupportNegativeIndices = false
	options.EscapeHTML = false
	return options
}

func marshalOperations(ops []Operation) ([]byte, error) {
	raws := make([]json.RawMessage, len(ops))
	for index, op := range ops {
		encoded, err := json.Marshal(operationObject(op))
		if err != nil {
			return nil, fmt.Errorf("encode json patch operation %d: %w", index, err)
		}
		raws[index] = encoded
	}
	return json.Marshal(raws)
}

func operationObject(op Operation) map[string]any {
	object := map[string]any{
		"op":   op.Op,
		"path": op.Path,
	}
	if op.From != "" {
		object["from"] = op.From
	}
	switch op.Op {
	case "add", "replace", "test":
		object["value"] = op.Value
	}
	return object
}

func decodeJSONDocument(document []byte) (map[string]any, error) {
	document = bytes.TrimSpace(document)
	if len(document) == 0 {
		return map[string]any{}, nil
	}
	object, err := decodeObject(document)
	if err != nil {
		return nil, fmt.Errorf("decode JSON: %w", err)
	}
	return object, nil
}

func decodeTOMLDocument(document []byte) (map[string]any, error) {
	document = bytes.TrimSpace(document)
	if len(document) == 0 {
		return map[string]any{}, nil
	}

	var object map[string]any
	if err := toml.Unmarshal(document, &object); err != nil {
		return nil, fmt.Errorf("decode TOML: %w", err)
	}
	if object == nil {
		return map[string]any{}, nil
	}
	normalized, ok := normalizeValue(object).(map[string]any)
	if !ok {
		return nil, fmt.Errorf("decode TOML: document root must be a table")
	}
	return normalized, nil
}
