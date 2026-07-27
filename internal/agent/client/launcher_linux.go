//go:build linux

package client

// Starts the agent in a new session without inheriting caller
// terminal streams or cancellation.

import (
	"fmt"
	"os"
	"os/exec"
	"syscall"

	"petris.dev/toby/internal/diagnostic"
)

func launchDetached(
	executable string,
	environment []string,
	logger *diagnostic.Logger,
) error {
	null, err := os.OpenFile(os.DevNull, os.O_RDWR, 0)
	if err != nil {
		return fmt.Errorf(
			"open null device for agent autostart: %w",
			err,
		)
	}
	defer func() {
		logger.DebugError(
			"close null device after agent autostart",
			null.Close(),
		)
	}()

	command := exec.Command(executable)
	command.Env = append([]string(nil), environment...)
	command.Dir = "/"
	command.Stdin = null
	command.Stdout = null
	command.Stderr = null
	command.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := command.Start(); err != nil {
		return fmt.Errorf("start detached agent: %w", err)
	}

	// Reap the child while this CLI remains alive. If the CLI exits first, the
	// detached service is reparented and remains independent of the launch.
	go func() {
		if waitErr := command.Wait(); waitErr != nil {
			logger.DebugError("detached agent exited", waitErr)
		}
	}()

	return nil
}
