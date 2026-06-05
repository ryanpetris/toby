// Package warning emits suppressible, namespaced warnings to stderr. Each warning
// carries a stable ID so users can silence it via settings.suppressWarnings; a
// Suppression records which IDs (or all of them) are silenced.
package warning

import (
	"fmt"
	"io"
	"os"
)

// Fprintf writes a warning line to stderr unless its ID is suppressed. A nil
// writer defaults to os.Stderr.
func Fprintf(stderr io.Writer, suppression Suppression, id ID, format string, args ...any) {
	if suppression.Suppresses(id) {
		return
	}
	if stderr == nil {
		stderr = os.Stderr
	}
	args = append([]any{id}, args...)
	_, _ = fmt.Fprintf(stderr, "toby: warning[%s]: "+format+"\n", args...)
}
