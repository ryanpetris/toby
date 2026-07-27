//go:build linux

package bwrap

// Configures Bubblewrap child streams for each supported terminal mode.

import (
	"fmt"
	"os"
	"os/exec"
	"syscall"

	"golang.org/x/term"
)

func configurePlainStreams(
	command *exec.Cmd,
	streams ProcessIO,
) {
	command.Stdin = streams.Stdin
	command.Stdout = streams.Stdout
	command.Stderr = streams.Stderr
}

func configureNonInteractive(command *exec.Cmd, streams ProcessIO) {
	configurePlainStreams(command, streams)
	discardNonInteractiveTerminalInput(command)

	// Create the process group and detach it from the caller's controlling
	// terminal before Bubblewrap executes. The fresh session prevents
	// noninteractive sandbox commands from reaching the caller through
	// /dev/tty and gives cancellation an immediately addressable process group.
	command.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
}

func discardNonInteractiveTerminalInput(command *exec.Cmd) {
	if input, ok := command.Stdin.(*os.File); ok &&
		input != nil &&
		term.IsTerminal(int(input.Fd())) {
		// Lifecycle and other noninteractive commands must not consume bytes
		// from the caller's terminal. A nil Cmd.Stdin becomes /dev/null while
		// explicit pipes and redirected files remain available to the command.
		command.Stdin = nil
	}
}

func configureDirectTerminal(command *exec.Cmd, streams ProcessIO) error {
	stdin, ok := streams.Stdin.(*os.File)
	if !ok || stdin == nil || !term.IsTerminal(int(stdin.Fd())) {
		return fmt.Errorf("direct-terminal stdin must be a terminal file")
	}

	command.Stdin = stdin
	command.Stdout = streams.Stdout
	command.Stderr = streams.Stderr

	return nil
}
