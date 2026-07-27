package status

// Operation identities and presentation modes shared by the startup service.

import "time"

// Mode is the selected startup presentation mode.
type Mode uint8

const (
	// ModeInteractive renders transient startup status on a terminal.
	ModeInteractive Mode = iota + 1
	// ModePlain emits periodic status without terminal control.
	ModePlain
	// ModeDebugTTY renders status while preserving all debug output.
	ModeDebugTTY
	// ModeQuiet suppresses non-foreground output.
	ModeQuiet
)

// OperationID identifies one startup operation within a launch.
type OperationID string

// Progress is one absolute operation progress snapshot.
type Progress struct {
	CompletedBytes int64
	TotalBytes     int64
	CompletedItems int64
	TotalItems     int64
	OCIAction      string
	OCIReference   string
}

type operationState struct {
	id         OperationID
	parent     OperationID
	scope      string
	label      string
	order      uint64
	running    bool
	failed     bool
	transcript boundedTranscript
	progress   *Progress

	lastPlainProgress time.Time
}
