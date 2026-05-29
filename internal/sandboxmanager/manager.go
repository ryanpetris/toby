package sandboxmanager

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"petris.dev/toby/internal/control"
)

type Runner struct {
	registry *Registry
}

func NewRunner(registry *Registry) *Runner {
	return &Runner{registry: registry}
}

func (r *Runner) Run(ctx context.Context, controlPath string) error {
	if r == nil || r.registry == nil {
		return fmt.Errorf("sandbox manager command registry is not configured")
	}
	return NewRuntime(r.registry).Run(ctx, controlPath)
}

type Runtime struct {
	peer     *control.Peer
	registry *Registry

	mu         sync.Mutex
	commands   map[string]*commandState
	foreground string
	terminate  chan struct{}
	once       sync.Once
}

type commandState struct {
	id         string
	cmd        *exec.Cmd
	cancel     context.CancelFunc
	foreground bool
	done       chan struct{}
}

func NewRuntime(registry *Registry) *Runtime {
	return &Runtime{registry: registry, commands: map[string]*commandState{}, terminate: make(chan struct{})}
}

func (r *Runtime) Run(ctx context.Context, controlPath string) error {
	if controlPath == "" {
		var err error
		controlPath, err = control.DefaultSocketPath()
		if err != nil {
			return err
		}
	}
	if _, err := os.Stat(controlPath); err != nil {
		return fmt.Errorf("toby sandbox manager must run inside a Toby sandbox: %s is not available", controlPath)
	}
	conn, err := net.Dial("unix", controlPath)
	if err != nil {
		return err
	}
	peer := control.NewPeer(ctx, conn, r.Handle)
	r.peer = peer
	peer.Start(nil)
	stopSignals := r.forwardSignals()
	defer stopSignals()
	if _, err := peer.Call(ctx, control.MethodContextInit, nil); err != nil {
		_ = peer.Close()
		return err
	}
	select {
	case <-r.terminate:
		_ = peer.Close()
		return nil
	case <-peer.Done():
		if err := peer.Err(); err != nil {
			return err
		}
		return nil
	case <-ctx.Done():
		r.stopCommands()
		_ = peer.Close()
		return ctx.Err()
	}
}

func (r *Runtime) Handle(ctx context.Context, data []byte) ([]byte, error) {
	req, err := control.DecodeRequest(data)
	if err != nil {
		return control.ResponseError(nil, control.CodeInvalidRequest, err.Error(), nil), syscall.EINVAL
	}
	return r.registry.Handle(ctx, r, req)
}

func (r *Runtime) commandRun(ctx context.Context, params control.CommandRunParams) error {
	commandCtx, cancel := context.WithCancel(ctx)
	cmd := exec.CommandContext(commandCtx, params.Argv[0], params.Argv[1:]...)
	cmd.Env = os.Environ()
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
	if err := r.addCommand(state); err != nil {
		cancel()
		if devNull != nil {
			_ = devNull.Close()
		}
		return err
	}
	if err := cmd.Start(); err != nil {
		r.removeCommand(state)
		cancel()
		if devNull != nil {
			_ = devNull.Close()
		}
		go r.sendCommandExit(params.CommandID, startErrorCode(err), "")
		return nil
	}
	go func() {
		defer close(state.done)
		defer cancel()
		defer r.removeCommand(state)
		if devNull != nil {
			defer devNull.Close()
		}
		err := cmd.Wait()
		exitCode, message := waitResult(commandCtx, err)
		r.sendCommandExit(params.CommandID, exitCode, message)
	}()
	return nil
}

func (r *Runtime) addCommand(state *commandState) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, exists := r.commands[state.id]; exists {
		return fmt.Errorf("command already exists: %s", state.id)
	}
	if state.foreground && r.foreground != "" {
		return fmt.Errorf("foreground command already running: %s", r.foreground)
	}
	r.commands[state.id] = state
	if state.foreground {
		r.foreground = state.id
	}
	return nil
}

func (r *Runtime) removeCommand(state *commandState) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.commands, state.id)
	if r.foreground == state.id {
		r.foreground = ""
	}
}

func (r *Runtime) sendCommandExit(commandID string, exitCode int, message string) {
	_, _ = r.peer.Call(context.Background(), control.MethodCommandExit, control.CommandExitParams{CommandID: commandID, ExitCode: exitCode, Error: message})
}

func (r *Runtime) stopCommands() {
	states := r.commandSnapshot()
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

func (r *Runtime) commandSnapshot() []*commandState {
	r.mu.Lock()
	defer r.mu.Unlock()
	states := make([]*commandState, 0, len(r.commands))
	for _, state := range r.commands {
		states = append(states, state)
	}
	return states
}

func (r *Runtime) signalForeground(sig os.Signal) {
	r.mu.Lock()
	if state := r.commands[r.foreground]; state != nil {
		r.mu.Unlock()
		if state.cmd.Process != nil {
			_ = state.cmd.Process.Signal(sig)
		}
		return
	}
	states := make([]*commandState, 0, len(r.commands))
	for _, state := range r.commands {
		states = append(states, state)
	}
	r.mu.Unlock()
	for _, state := range states {
		if state.cmd.Process != nil {
			_ = state.cmd.Process.Signal(sig)
		}
	}
}

func (r *Runtime) signalTerminate() {
	r.once.Do(func() { close(r.terminate) })
}

func (r *Runtime) forwardSignals() func() {
	signals := make(chan os.Signal, 4)
	signal.Notify(signals, syscall.SIGINT, syscall.SIGTERM, syscall.SIGHUP, syscall.SIGQUIT)
	done := make(chan struct{})
	go func() {
		defer close(done)
		for sig := range signals {
			r.signalForeground(sig)
		}
	}()
	return func() {
		signal.Stop(signals)
		close(signals)
		<-done
	}
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
