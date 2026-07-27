package tobymcp

// Strictly decodes native session snapshots, rejecting duplicate, unknown, and
// trailing JSON fields before the snapshot reaches an introspection service.

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
)

const maxSessionSnapshotBytes = 256 << 10

// DecodeSessionSnapshot strictly decodes, validates, and detaches one native
// run snapshot.
func DecodeSessionSnapshot(data json.RawMessage) (SessionSnapshot, error) {
	if len(data) == 0 {
		return SessionSnapshot{}, fmt.Errorf("session snapshot is empty")
	}
	if len(data) > maxSessionSnapshotBytes {
		return SessionSnapshot{}, fmt.Errorf(
			"session snapshot exceeds %d bytes",
			maxSessionSnapshotBytes,
		)
	}
	if err := rejectSnapshotDuplicateFields(data); err != nil {
		return SessionSnapshot{}, fmt.Errorf(
			"decode session snapshot: %w",
			err,
		)
	}

	var snapshot SessionSnapshot
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&snapshot); err != nil {
		return SessionSnapshot{}, fmt.Errorf(
			"decode session snapshot: %w",
			err,
		)
	}
	if token, err := decoder.Token(); !errors.Is(err, io.EOF) {
		if err != nil {
			return SessionSnapshot{}, fmt.Errorf(
				"decode trailing session snapshot data: %w",
				err,
			)
		}

		return SessionSnapshot{}, fmt.Errorf(
			"session snapshot has trailing value %v",
			token,
		)
	}
	if err := snapshot.Validate(); err != nil {
		return SessionSnapshot{}, err
	}

	return snapshot.Clone(), nil
}

func rejectSnapshotDuplicateFields(data []byte) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	if err := scanSnapshotJSONValue(decoder, "$"); err != nil {
		return err
	}
	if token, err := decoder.Token(); !errors.Is(err, io.EOF) {
		if err != nil {
			return err
		}

		return fmt.Errorf("trailing JSON value %v", token)
	}

	return nil
}

func scanSnapshotJSONValue(
	decoder *json.Decoder,
	location string,
) error {
	token, err := decoder.Token()
	if err != nil {
		return err
	}

	delimiter, composite := token.(json.Delim)
	if !composite {
		return nil
	}

	switch delimiter {
	case '{':
		seen := make(map[string]struct{})
		for decoder.More() {
			nameToken, err := decoder.Token()
			if err != nil {
				return err
			}
			name, ok := nameToken.(string)
			if !ok {
				return fmt.Errorf(
					"object key at %s is not a string",
					location,
				)
			}
			if _, duplicate := seen[name]; duplicate {
				return fmt.Errorf(
					"duplicate object key %q at %s",
					name,
					location,
				)
			}
			seen[name] = struct{}{}

			if err := scanSnapshotJSONValue(
				decoder,
				location+"."+name,
			); err != nil {
				return err
			}
		}

		closeToken, err := decoder.Token()
		if err != nil {
			return err
		}
		if closeDelimiter, ok := closeToken.(json.Delim); !ok ||
			closeDelimiter != '}' {
			return fmt.Errorf("object at %s is not closed", location)
		}
	case '[':
		index := 0
		for decoder.More() {
			if err := scanSnapshotJSONValue(
				decoder,
				fmt.Sprintf("%s[%d]", location, index),
			); err != nil {
				return err
			}
			index++
		}

		closeToken, err := decoder.Token()
		if err != nil {
			return err
		}
		if closeDelimiter, ok := closeToken.(json.Delim); !ok ||
			closeDelimiter != ']' {
			return fmt.Errorf("array at %s is not closed", location)
		}
	default:
		return fmt.Errorf(
			"unexpected JSON delimiter %q at %s",
			delimiter,
			location,
		)
	}

	return nil
}
