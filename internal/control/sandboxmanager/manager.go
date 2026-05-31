package sandboxmanager

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"strings"
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
	env        map[string]string
}

type commandState struct {
	id         string
	cmd        *exec.Cmd
	cancel     context.CancelFunc
	foreground bool
	done       chan struct{}
}

func NewRuntime(registry *Registry) *Runtime {
	return &Runtime{registry: registry, commands: map[string]*commandState{}, terminate: make(chan struct{}), env: environmentFromList(os.Environ())}
}

func (r *Runtime) Run(ctx context.Context, controlPath string) error {
	endpoint, err := control.DefaultEndpoint()
	if err != nil {
		return err
	}
	conn, err := control.DialEndpoint(endpoint)
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
	argv := r.commandArgv(params)
	commandCtx, cancel := context.WithCancel(ctx)
	cmd := exec.CommandContext(commandCtx, argv[0], argv[1:]...)
	cmd.Env = r.environmentList()
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

func (r *Runtime) commandArgv(params control.CommandRunParams) []string {
	if len(params.Argv) > 0 {
		return params.Argv
	}
	return r.defaultShellCommand()
}

func (r *Runtime) defaultShellCommand() []string {
	shell, _ := r.getEnvironment("SHELL")
	if shell := executableShell(shell); shell != "" {
		return []string{shell, "-i"}
	}
	return []string{"/bin/sh", "-i"}
}

func (r *Runtime) environmentSnapshot() map[string]string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return cloneEnvironment(r.env)
}

func (r *Runtime) getEnvironment(name string) (string, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	value, ok := r.env[name]
	return value, ok
}

func (r *Runtime) setEnvironment(name, value string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.env == nil {
		r.env = map[string]string{}
	}
	if value == "" {
		delete(r.env, name)
		return
	}
	r.env[name] = value
}

func (r *Runtime) environmentList() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	values := make([]string, 0, len(r.env))
	for name, value := range r.env {
		values = append(values, name+"="+value)
	}
	return values
}

func environmentFromList(values []string) map[string]string {
	env := make(map[string]string, len(values))
	for _, item := range values {
		name, value, ok := strings.Cut(item, "=")
		if !ok {
			continue
		}
		env[name] = value
	}
	return env
}

func cloneEnvironment(env map[string]string) map[string]string {
	clone := make(map[string]string, len(env))
	for name, value := range env {
		clone[name] = value
	}
	return clone
}

func executableShell(shell string) string {
	if shell == "" {
		return ""
	}
	path, err := exec.LookPath(shell)
	if err != nil {
		return ""
	}
	info, err := os.Stat(path)
	if err != nil || info.IsDir() || info.Mode()&0o111 == 0 {
		return ""
	}
	return path
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
