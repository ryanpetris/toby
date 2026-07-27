//go:build !linux

package sandbox

// Reports that Toby sandbox helpers require Linux.

import (
	"os"

	"petris.dev/toby/internal/diagnostic"
)

// Run reports the unsupported operating system.
func Run() {
	_, err := os.Stderr.WriteString("tobys requires Linux\n")
	diagnostic.DiscardError(
		"the unsupported-platform result is already determined",
		"write sandbox helper platform error",
		err,
	)
	os.Exit(1)
}
