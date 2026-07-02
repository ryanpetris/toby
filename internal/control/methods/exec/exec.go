// Package exec implements the exec.* control methods inside the sandbox home manager:
// exec.run runs a non-interactive command (installs, upgrades, init scripts) as the
// invoking user, streaming stdout/stderr back to the daemon as exec.output
// notifications and replying with the exit code. The home manager runs as root, so it
// drops to the requested uid:gid via a process credential.
package exec

import (
	"context"
	"io"
	"os/exec"
	"sync"
	"syscall"

	"petris.dev/toby/internal/control"
)

const (
	MethodRun    = "exec.run"
	MethodOutput = "exec.output"

	StreamStdout = "stdout"
	StreamStderr = "stderr"
)

// RunParams describes a command to run. ExecID correlates the streamed output with the
// daemon's call.
type RunParams struct {
	ExecID string   `json:"execID"`
	Argv   []string `json:"argv"`
	Env    []string `json:"env,omitempty"`
	Cwd    string   `json:"cwd,omitempty"`
	UID    int      `json:"uid"`
	GID    int      `json:"gid"`
}

// OutputParams is one streamed chunk of command output.
type OutputParams struct {
	ExecID string `json:"execID"`
	Stream string `json:"stream"`
	Data   []byte `json:"data"`
}

// RunResult reports the command's exit code.
type RunResult struct {
	ExitCode int `json:"exitCode"`
}

var _ control.Capability = (*Service)(nil)

// Service runs commands and streams their output via emit.
type Service struct {
	emit func(method string, params any) error
}

// New builds the exec capability. emit sends a notification over the control peer (the
// home manager wires it to peer.Notify).
func New(emit func(method string, params any) error) *Service {
	return &Service{emit: emit}
}

func (s *Service) Methods() []control.Method {
	return []control.Method{{Name: MethodRun, Handle: s.handleRun}}
}

func (s *Service) handleRun(ctx context.Context, req control.RPCRequest) ([]byte, error) {
	params, err := control.DecodeParams[RunParams](req.Params)
	if err != nil || len(params.Argv) == 0 {
		return control.ResponseError(req.ID, control.CodeInvalidParams, "argv is required", nil), syscall.EINVAL
	}

	code, err := s.run(ctx, params)
	if err != nil {
		return control.ResponseError(req.ID, control.CodeInternalError, err.Error(), nil), syscall.EIO
	}
	return control.ResponseOK(req.ID, RunResult{ExitCode: code}), nil
}

func (s *Service) run(ctx context.Context, params RunParams) (int, error) {
	cmd := exec.CommandContext(ctx, params.Argv[0], params.Argv[1:]...)
	cmd.Env = params.Env
	cmd.Dir = params.Cwd
	// The home manager is root; drop to the invoking user so installed files are owned
	// correctly. UID 0 keeps root (no credential).
	if params.UID != 0 || params.GID != 0 {
		cmd.SysProcAttr = &syscall.SysProcAttr{Credential: &syscall.Credential{Uid: uint32(params.UID), Gid: uint32(params.GID)}}
	}

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return 1, err
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return 1, err
	}
	if err := cmd.Start(); err != nil {
		return 1, err
	}

	var wg sync.WaitGroup
	wg.Add(2)
	go s.pump(params.ExecID, StreamStdout, stdout, &wg)
	go s.pump(params.ExecID, StreamStderr, stderr, &wg)
	wg.Wait()

	if err := cmd.Wait(); err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			return exitErr.ExitCode(), nil
		}
		return 1, err
	}
	return 0, nil
}

// pump forwards output chunks as exec.output notifications until the pipe closes.
func (s *Service) pump(execID, stream string, r io.Reader, wg *sync.WaitGroup) {
	defer wg.Done()
	buf := make([]byte, 32*1024)
	for {
		n, err := r.Read(buf)
		if n > 0 && s.emit != nil {
			chunk := append([]byte(nil), buf[:n]...)
			_ = s.emit(MethodOutput, OutputParams{ExecID: execID, Stream: stream, Data: chunk})
		}
		if err != nil {
			return
		}
	}
}
