// Running the foreground tool. The daemon hands back the id of a tool container whose
// main process is `sandbox launch` (it execs the actual tool as PID 1). The client
// attaches to that container and starts it, so the interactive PTY, raw mode, resize,
// and the approval modal all attach to the user's real terminal — the daemon never
// allocates a PTY. The register callback wires the run's approval prompter so
// daemon-side services can prompt the user through this client (see prompter.go).

package client

import (
	"context"

	sandboxapi "petris.dev/toby/sandbox"
	"petris.dev/toby/sandbox/runtime"
)

// runForeground attaches to the tool container, starts it, and returns the tool's
// exit code.
func (s *Service) runForeground(ctx context.Context, containerID string, managed bool, register func(sandboxapi.ApprovalPrompter)) (int, error) {
	cli, err := s.engine.Client(ctx)
	if err != nil {
		return 1, err
	}
	return runtime.AttachAndRun(ctx, cli, containerID, runtime.AttachOptions{
		Managed:          managed,
		RegisterPrompter: register,
	})
}
