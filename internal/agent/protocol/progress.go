package protocol

// Defines bounded internal startup progress records shared by agent resource
// backends and their disk logs.

import "fmt"

// MaxProgressOutputBytes bounds one raw subprocess-output record. Producers
// split larger writes before reporting them.
const MaxProgressOutputBytes = 32 << 10

// AcquireProgress is one ordered startup event. It is an internal producer
// contract rather than an agent wire response.
type AcquireProgress struct {
	Sequence  uint64       `json:"sequence"`
	Operation OperationID  `json:"operation"`
	Kind      ProgressKind `json:"kind"`
	Source    string       `json:"source"`
	Text      string       `json:"text"`
	Stream    OutputStream `json:"stream,omitempty"`
	Data      []byte       `json:"data,omitempty"`
}

// ProgressKind identifies one startup event shape.
type ProgressKind string

const (
	// ProgressStep updates the current startup step.
	ProgressStep ProgressKind = "step"
	// ProgressOutput carries startup command output.
	ProgressOutput ProgressKind = "output"
	// ProgressComplete reports successful acquisition.
	ProgressComplete ProgressKind = "complete"
	// ProgressFailure reports failed acquisition.
	ProgressFailure ProgressKind = "failure"
)

// OutputStream identifies the inherited subprocess stream carried by raw
// progress output.
type OutputStream string

const (
	// OutputStdout identifies standard output.
	OutputStdout OutputStream = "stdout"
	// OutputStderr identifies standard error.
	OutputStderr OutputStream = "stderr"
)

func (s OutputStream) validate() error {
	switch s {
	case OutputStdout, OutputStderr:
		return nil
	default:
		return fmt.Errorf("unknown output stream %q", s)
	}
}
