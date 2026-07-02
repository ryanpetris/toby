// Detached daemon spawn, shared by transport implementations. The spawned process
// inherits the current environment (so TOBY_TRANSPORT and config selection carry
// over) and runs in its own session, outliving the launching client.

package transport

import (
	"os"
	"os/exec"
	"syscall"
)

// SpawnDaemon starts a detached `toby daemon` in its own session with null stdio.
func SpawnDaemon() error {
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	null, err := os.OpenFile(os.DevNull, os.O_RDWR, 0)
	if err != nil {
		return err
	}
	defer null.Close()

	cmd := exec.Command(exe, "daemon")
	cmd.Stdin, cmd.Stdout, cmd.Stderr = null, null, null
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := cmd.Start(); err != nil {
		return err
	}
	return cmd.Process.Release()
}
