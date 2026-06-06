// Package status reports a launch's startup progress on a single, self-erasing
// terminal line. The Service is an fx singleton any component can write to while
// a session starts up: Set posts the message the user should see right now, and
// the latest message replaces the previous one. Stop erases the line before the
// launched tool takes over the terminal, so nothing is left behind. When stderr
// is not a terminal the Service is inert and prints nothing.
//
// In plain mode (used for debug launches), the Service instead writes each status
// as its own line that is never overwritten or erased, so every step is preserved
// on screen alongside the debug logging.
package status

import (
	"fmt"
	"io"
	"os"
)

// Service is the startup-status channel for one launch. It owns a Spinner over
// stderr and exposes a tiny write-and-clear API; callers never touch the spinner
// directly. Set is safe to call from anywhere and from any goroutine.
type Service struct {
	out     io.Writer
	spinner *Spinner
	plain   bool
}

// NewService builds the Service over stderr. Startup status is not application
// output, so it goes to stderr and leaves stdout clean for the launched tool.
func NewService() *Service {
	return &Service{out: os.Stderr, spinner: newSpinner(os.Stderr)}
}

// SetPlain switches the Service between the animated single-line spinner (the
// default) and plain mode, in which each Set writes its message as its own line
// that is never overwritten or erased and Stop does nothing. Plain mode is used
// for debug launches so every step stays on screen alongside the debug logging.
// Call it before the first Set.
func (s *Service) SetPlain(plain bool) {
	s.plain = plain
}

// Set posts text as the current status: in spinner mode it starts the indicator
// on the first call and updates it in place thereafter; in plain mode it writes
// text as its own preserved line. Callers pass the bare step (e.g. "Starting
// sandbox"); Set appends the trailing ellipsis so every message reads uniformly.
func (s *Service) Set(text string) {
	text += "..."
	if s.plain {
		fmt.Fprintln(s.out, text)
		return
	}
	s.spinner.Start(text)
	s.spinner.Update(text)
}

// Stop erases the status line and stops the indicator. It is idempotent, so the
// launch can defer it as a safety net and still call it explicitly just before
// handing the terminal to the tool. In plain mode it is a no-op, leaving every
// printed step on screen.
func (s *Service) Stop() {
	if s.plain {
		return
	}
	s.spinner.Stop()
}
