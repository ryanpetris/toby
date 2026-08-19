package tobymcp

// Strictly decodes native session snapshots, rejecting duplicate, unknown, and
// trailing JSON fields before the snapshot reaches an introspection service.

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"

	configfile "petris.dev/toby/internal/config/file"
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
	if err := configfile.RejectDuplicateFields(data); err != nil {
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
