//go:build linux

// Package sandbox provides the minimal command surface mounted inside a Toby
// sandbox.
package sandbox

import (
	"fmt"
	"io"
	"os"

	"petris.dev/toby/internal/diagnostic"
	"petris.dev/toby/internal/executable"
	"petris.dev/toby/internal/sandbox/bwrap"
	"petris.dev/toby/internal/version"
)

const usage = `Usage:
  tobys resource connect -- <client-resource-id>
  tobys exec <ready-fd|-1> <stderr-fd|-1> <signal-fd|-1> -- <command> [args...]
`

// Run dispatches one sandbox-only helper command.
func Run() {
	if err := executable.CheckSandboxUnprivileged(); err != nil {
		writeSandboxResult(os.Stderr, err.Error()+"\n")
		os.Exit(1)
	}

	os.Exit(Execute(os.Args, os.Stdin, os.Stdout, os.Stderr))
}

// Execute dispatches one sandbox-only helper command and returns its exit code.
func Execute(
	arguments []string,
	stdin io.ReadCloser,
	stdout io.Writer,
	stderr io.Writer,
) int {
	if code, handled := bwrap.DispatchExec(
		arguments,
		os.Getenv("TOBY_SANDBOX"),
		stderr,
	); handled {
		return code
	}
	if code, handled := DispatchResourceConnector(
		arguments,
		stdin,
		stdout,
		stderr,
	); handled {
		return code
	}

	if len(arguments) == 1 ||
		(len(arguments) == 2 &&
			(arguments[1] == "help" ||
				arguments[1] == "--help" ||
				arguments[1] == "-h")) {
		writeSandboxResult(stdout, usage)
		return 0
	}
	if len(arguments) == 2 &&
		(arguments[1] == "version" || arguments[1] == "--version") {
		writeSandboxResult(stdout, version.String()+"\n")
		return 0
	}

	writeSandboxResult(stderr, fmt.Sprintf(
		"unsupported tobys command\n\n%s",
		usage,
	))
	return 2
}

func writeSandboxResult(output io.Writer, value string) {
	if output == nil {
		return
	}
	_, err := io.WriteString(output, value)
	diagnostic.DiscardError(
		"the sandbox helper result is already determined",
		"write sandbox helper output",
		err,
	)
}
