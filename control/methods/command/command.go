// Package command implements the command.run control method: running a process
// inside the sandbox and reporting its exit via a command.exit notification back
// to the host. The Service owns the running-command registry and foreground
// tracking, reads the command environment from the env capability, and emits exits
// through a Sender (the live control peer, set when the connection comes up).
package command

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"petris.dev/toby/control"
	envcap "petris.dev/toby/control/methods/env"
)

// Sender sends a request over the live control connection. *control.Peer satisfies
// it; the sandbox runtime sets it once the peer is established.
type Sender interface {
	Call(ctx context.Context, method string, params any) (control.RPCResponse, error)
}

// Service runs sandbox commands and tracks them.
type Service struct {
	env *envcap.Service

	mu         sync.Mutex
	commands   map[string]*commandState
	foreground string

	senderMu sync.Mutex
	sender   Sender
}

type commandState struct {
	id         string
	cmd        *exec.Cmd
	cancel     context.CancelFunc
	foreground bool
	done       chan struct{}
}

var _ control.Capability = (*Service)(nil)

// New constructs the command Service backed by the env capability.
func New(environment *envcap.Service) *Service {
	return &Service{env: environment, commands: map[string]*commandState{}}
}

// SetSender supplies the connection used to report command exits. Called by the
// sandbox runtime once the control peer is established.
func (s *Service) SetSender(sender Sender) {
	s.senderMu.Lock()
	s.sender = sender
	s.senderMu.Unlock()
}

// Methods reports the command.run method this capability handles.
func (s *Service) Methods() []control.Method {
	return []control.Method{{Name: MethodRun, Handle: s.handleRun}}
}

func (s *Service) handleRun(ctx context.Context, req control.RPCRequest) ([]byte, error) {
	params, err := DecodeRunParams(req.Params)
	if err != nil {
		return control.ResponseError(req.ID, control.CodeInvalidParams, err.Error(), nil), syscall.EINVAL
	}
	if err := s.run(ctx, params); err != nil {
		return control.ResponseError(req.ID, control.CodeInvalidParams, err.Error(), nil), syscall.EINVAL
	}
	return control.ResponseOK(req.ID, control.EmptyResult{}), nil
}

func (s *Service) run(ctx context.Context, params RunParams) error {
	env := s.env.CommandEnvironment()
	argv := s.commandArgv(params, env)
	var ok bool
	argv, ok = resolveCommandArgv(argv, env)
	if !ok {
		go s.sendCommandExit(params.CommandID, 127, "")
		return nil
	}
	commandCtx, cancel := context.WithCancel(ctx)
	cmd := exec.CommandContext(commandCtx, argv[0], argv[1:]...)
	cmd.Env = envList(env)
	credential, err := commandCredential(params)
	if err != nil {
		cancel()
		return err
	}
	if credential != nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{Credential: credential}
	}
	if params.Foreground {
		cmd.Stdin = os.Stdin
	}
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	var devNull *os.File
	if params.HideOutput {
		var err error
		devNull, err = os.OpenFile(os.DevNull, os.O_RDWR, 0)
		if err != nil {
			cancel()
			return err
		}
		cmd.Stdout = devNull
		cmd.Stderr = devNull
	}
	state := &commandState{id: params.CommandID, cmd: cmd, cancel: cancel, foreground: params.Foreground, done: make(chan struct{})}
	if err := s.addCommand(state); err != nil {
		cancel()
		if devNull != nil {
			_ = devNull.Close()
		}
		return err
	}
	if err := cmd.Start(); err != nil {
		s.removeCommand(state)
		cancel()
		if devNull != nil {
			_ = devNull.Close()
		}
		go s.sendCommandExit(params.CommandID, startErrorCode(err), "")
		return nil
	}
	go func() {
		defer close(state.done)
		defer cancel()
		defer s.removeCommand(state)
		if devNull != nil {
			defer devNull.Close()
		}
		err := cmd.Wait()
		exitCode, message := waitResult(commandCtx, err)
		s.sendCommandExit(params.CommandID, exitCode, message)
	}()
	return nil
}

func (s *Service) commandArgv(params RunParams, env map[string]string) []string {
	if len(params.Argv) > 0 {
		return params.Argv
	}
	return defaultShellCommand(env)
}

func defaultShellCommand(env map[string]string) []string {
	if shell := executableShell(env["SHELL"], env["PATH"]); shell != "" {
		return []string{shell, "-i"}
	}
	return []string{"/bin/sh", "-i"}
}

func (s *Service) sendCommandExit(commandID string, exitCode int, message string) {
	s.senderMu.Lock()
	sender := s.sender
	s.senderMu.Unlock()
	if sender == nil {
		return
	}
	_, _ = sender.Call(context.Background(), MethodExit, ExitParams{CommandID: commandID, ExitCode: exitCode, Error: message})
}

func (s *Service) addCommand(state *commandState) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, exists := s.commands[state.id]; exists {
		return fmt.Errorf("command already exists: %s", state.id)
	}
	if state.foreground && s.foreground != "" {
		return fmt.Errorf("foreground command already running: %s", s.foreground)
	}
	s.commands[state.id] = state
	if state.foreground {
		s.foreground = state.id
	}
	return nil
}

func (s *Service) removeCommand(state *commandState) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.commands, state.id)
	if s.foreground == state.id {
		s.foreground = ""
	}
}

func (s *Service) commandSnapshot() []*commandState {
	s.mu.Lock()
	defer s.mu.Unlock()
	states := make([]*commandState, 0, len(s.commands))
	for _, state := range s.commands {
		states = append(states, state)
	}
	return states
}

// StopAll cancels and terminates every running command, escalating to SIGKILL.
func (s *Service) StopAll() {
	states := s.commandSnapshot()
	for _, state := range states {
		state.cancel()
		if state.cmd.Process != nil {
			_ = state.cmd.Process.Signal(syscall.SIGTERM)
		}
	}
	deadline := time.After(2 * time.Second)
	for _, state := range states {
		select {
		case <-state.done:
		case <-deadline:
			for _, remaining := range states {
				if remaining.cmd.Process != nil {
					_ = remaining.cmd.Process.Kill()
				}
			}
			killDeadline := time.After(time.Second)
			for _, remaining := range states {
				select {
				case <-remaining.done:
				case <-killDeadline:
					return
				}
			}
			return
		}
	}
}

// SignalForeground forwards a signal to the foreground command, or to all running
// commands when there is no distinguished foreground.
func (s *Service) SignalForeground(sig os.Signal) {
	s.mu.Lock()
	if state := s.commands[s.foreground]; state != nil {
		s.mu.Unlock()
		if state.cmd.Process != nil {
			_ = state.cmd.Process.Signal(sig)
		}
		return
	}
	states := make([]*commandState, 0, len(s.commands))
	for _, state := range s.commands {
		states = append(states, state)
	}
	s.mu.Unlock()
	for _, state := range states {
		if state.cmd.Process != nil {
			_ = state.cmd.Process.Signal(sig)
		}
	}
}

func envList(env map[string]string) []string {
	values := make([]string, 0, len(env))
	for name, value := range env {
		values = append(values, name+"="+value)
	}
	return values
}

func waitResult(ctx context.Context, err error) (int, string) {
	if err == nil {
		return 0, ""
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		code := exitErr.ExitCode()
		if code < 0 {
			code = 130
		}
		return code, ""
	}
	if errors.Is(err, context.Canceled) || errors.Is(ctx.Err(), context.Canceled) {
		return 130, ""
	}
	return 1, err.Error()
}

func commandCredential(params RunParams) (*syscall.Credential, error) {
	if params.UID == control.HostUser || params.GID == control.HostGroup {
		return nil, fmt.Errorf("unresolved host command identity")
	}
	if params.UID < 0 {
		return nil, fmt.Errorf("invalid command uid")
	}
	if params.GID < 0 {
		return nil, fmt.Errorf("invalid command gid")
	}
	groups := make([]uint32, 0, len(params.Groups))
	for _, group := range params.Groups {
		if group == control.HostGroup {
			return nil, fmt.Errorf("unresolved host command group")
		}
		if group < 0 {
			return nil, fmt.Errorf("invalid supplementary gid")
		}
		groups = append(groups, uint32(group))
	}
	if os.Geteuid() != 0 {
		return nil, nil
	}
	return &syscall.Credential{Uid: uint32(params.UID), Gid: uint32(params.GID), Groups: groups}, nil
}

func startErrorCode(err error) int {
	if errors.Is(err, exec.ErrNotFound) || errors.Is(err, os.ErrNotExist) {
		return 127
	}
	if errors.Is(err, os.ErrPermission) {
		return 126
	}
	var pathErr *os.PathError
	if errors.As(err, &pathErr) {
		if errors.Is(pathErr.Err, os.ErrNotExist) {
			return 127
		}
		if errors.Is(pathErr.Err, os.ErrPermission) {
			return 126
		}
	}
	if errors.Is(err, io.EOF) {
		return 1
	}
	return 126
}

func resolveCommandArgv(argv []string, env map[string]string) ([]string, bool) {
	if len(argv) == 0 {
		return argv, true
	}
	path, ok := resolveExecutable(argv[0], env["PATH"])
	if !ok {
		return nil, false
	}
	resolved := append([]string(nil), argv...)
	resolved[0] = path
	return resolved, true
}

func resolveExecutable(name, pathEnv string) (string, bool) {
	if name == "" || strings.ContainsRune(name, 0) {
		return "", false
	}
	if strings.ContainsRune(name, os.PathSeparator) {
		return name, true
	}
	for _, dir := range filepath.SplitList(pathEnv) {
		if dir == "" {
			dir = "."
		}
		path := filepath.Join(dir, name)
		info, err := os.Stat(path)
		if err == nil && !info.IsDir() && info.Mode()&0o111 != 0 {
			return path, true
		}
	}
	return "", false
}

func executableShell(shell, pathEnv string) string {
	if shell == "" {
		return ""
	}
	path, ok := resolveExecutable(shell, pathEnv)
	if !ok {
		return ""
	}
	info, err := os.Stat(path)
	if err != nil || info.IsDir() || info.Mode()&0o111 == 0 {
		return ""
	}
	return path
}
