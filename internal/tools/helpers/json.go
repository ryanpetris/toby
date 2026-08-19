package helpers

// JSON rendering helpers for generated tool configuration files.

import "encoding/json"

// MarshalJSON encodes value as indented JSON with a trailing newline.
func MarshalJSON(value any) ([]byte, error) {
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(data, '\n'), nil
}
