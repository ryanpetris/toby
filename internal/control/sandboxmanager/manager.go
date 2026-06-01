package sandboxmanager

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
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
	env := r.commandEnvironmentSnapshot()
	argv := r.commandArgv(params, env)
	var ok bool
	argv, ok = resolveCommandArgv(argv, env)
	if !ok {
		go r.sendCommandExit(params.CommandID, 127, "")
		return nil
	}
	commandCtx, cancel := context.WithCancel(ctx)
	cmd := exec.CommandContext(commandCtx, argv[0], argv[1:]...)
	cmd.Env = environmentList(env)
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

func (r *Runtime) commandArgv(params control.CommandRunParams, env map[string]string) []string {
	if len(params.Argv) > 0 {
		return params.Argv
	}
	return r.defaultShellCommand(env)
}

func (r *Runtime) defaultShellCommand(env map[string]string) []string {
	shell := env["SHELL"]
	if shell := executableShell(shell, env["PATH"]); shell != "" {
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
	return environmentList(r.env)
}

func (r *Runtime) commandEnvironmentList() []string {
	return environmentList(r.commandEnvironmentSnapshot())
}

func (r *Runtime) commandEnvironmentSnapshot() map[string]string {
	env := r.environmentSnapshot()
	delete(env, control.EnvControlHost)
	delete(env, control.EnvControlToken)
	return env
}

func environmentList(env map[string]string) []string {
	values := make([]string, 0, len(env))
	for name, value := range env {
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

func commandCredential(params control.CommandRunParams) (*syscall.Credential, error) {
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
		if params.UID == os.Getuid() && params.GID == os.Getgid() {
			return nil, nil
		}
		return nil, fmt.Errorf("cannot set command credentials as non-root")
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
