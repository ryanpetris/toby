package configpatch

// Declares reusable document edits: Ensure/Remove intents and raw RFC 6902
// operations.

import (
	"encoding/json"
	"fmt"
)

// Value is one member that must be present or absent at a JSON Pointer path.
type Value struct {
	Path  string
	Value any
}

// Operation is one RFC 6902 JSON Patch operation.
type Operation struct {
	Op    string
	Path  string
	From  string
	Value any
}

// Patch is a document edit. Raw Operations are applied first. Ensure and
// Remove are then compiled against that result and applied.
type Patch struct {
	Ensure     []Value
	Remove     []Value
	Operations []Operation
}

// Empty reports that the patch has no intents and no operations.
func (p Patch) Empty() bool {
	return len(p.Ensure) == 0 && len(p.Remove) == 0 && len(p.Operations) == 0
}

// Validate reports whether the patch can be compiled and applied.
func (p Patch) Validate() error {
	for index, item := range p.Ensure {
		if err := validateIntent("ensure", item); err != nil {
			return fmt.Errorf("ensure %d: %w", index, err)
		}
	}
	for index, item := range p.Remove {
		if err := validateIntent("remove", item); err != nil {
			return fmt.Errorf("remove %d: %w", index, err)
		}
	}
	for _, ensure := range p.Ensure {
		for _, remove := range p.Remove {
			if ensure.Path == remove.Path && equalValue(ensure.Value, remove.Value) {
				return fmt.Errorf("ensure and remove both target %q", ensure.Path)
			}
		}
	}
	for index, op := range p.Operations {
		if err := validateOperation(op); err != nil {
			return fmt.Errorf("operation %d: %w", index, err)
		}
	}
	return nil
}

// Clone returns a detached copy of the patch.
func (p Patch) Clone() Patch {
	return Patch{
		Ensure:     cloneValues(p.Ensure),
		Remove:     cloneValues(p.Remove),
		Operations: cloneOperations(p.Operations),
	}
}

func validateIntent(kind string, item Value) error {
	if _, err := parsePointer(item.Path); err != nil {
		return err
	}
	if _, err := json.Marshal(item.Value); err != nil {
		return fmt.Errorf("%s value at %q is not JSON-serializable: %w", kind, item.Path, err)
	}
	return nil
}

func validateOperation(op Operation) error {
	switch op.Op {
	case "add", "remove", "replace", "move", "copy", "test":
	default:
		return fmt.Errorf("unsupported op %q", op.Op)
	}
	if op.Path != "" {
		if _, err := parsePointer(op.Path); err != nil {
			return err
		}
	}
	if (op.Op == "move" || op.Op == "copy") && op.From == "" {
		return fmt.Errorf("operation %q requires from", op.Op)
	}
	if op.From != "" {
		if _, err := parsePointer(op.From); err != nil {
			return fmt.Errorf("from: %w", err)
		}
	}
	if _, err := json.Marshal(op.Value); err != nil {
		return fmt.Errorf("operation value is not JSON-serializable: %w", err)
	}
	return nil
}

func cloneValues(values []Value) []Value {
	if values == nil {
		return nil
	}
	clone := make([]Value, len(values))
	for index, item := range values {
		clone[index] = Value{
			Path:  item.Path,
			Value: cloneJSONValue(item.Value),
		}
	}
	return clone
}

func cloneOperations(ops []Operation) []Operation {
	if ops == nil {
		return nil
	}
	clone := make([]Operation, len(ops))
	for index, op := range ops {
		clone[index] = Operation{
			Op:    op.Op,
			Path:  op.Path,
			From:  op.From,
			Value: cloneJSONValue(op.Value),
		}
	}
	return clone
}
