package sandbox

import (
	"context"
	"fmt"
	"strings"
	"sync"

	"petris.dev/toby/container/mount"
	"petris.dev/toby/control"
	"petris.dev/toby/control/host"
	"petris.dev/toby/control/methods/command"
	"petris.dev/toby/diagnostic/exitcode"
	"petris.dev/toby/platform/environ"
	sandboxapi "petris.dev/toby/sandbox"
	"petris.dev/toby/tools"
)

type Service struct {
	mu          sync.Mutex
	instance    Instance
	client      *host.SandboxClient
	exits       *CommandExits
	managerExit *ManagerExit
	env         environ.Environment
	mcpURL      string
	mounts      *mount.Service
	started     bool
}

type SandboxService = Service

func newService(mounts *mount.Service) *Service { return &Service{mounts: mounts} }

// mountRunner adapts the sandbox service to mount.Executor for setup hooks.
type mountRunner struct{ s *Service }

func (r mountRunner) Exec(ctx context.Context, argv []string, root bool) (int, error) {
	return r.s.Exec(ctx, argv, sandboxapi.ExecOptions{Root: root, HideOutput: true})
}

var _ sandboxapi.Service = (*Service)(nil)

func (s *Service) Prepare(instance Instance) {
	s.mu.Lock()
	s.instance = instance
	s.client = nil
	s.exits = nil
	s.managerExit = nil
	s.env = nil
	s.mcpURL = ""
	s.started = false
	s.mu.Unlock()
}

func (s *Service) ConfigureMounts(opts *tools.Options) error {
	s.mu.Lock()
	instance := s.instance
	mounts := s.mounts
	s.mu.Unlock()
	if instance == nil {
		return fmt.Errorf("sandbox is not configured")
	}
	if mounts == nil {
		return fmt.Errorf("mount service is not configured")
	}
	mountProfile := ""
	toolProfiles := map[string]string(nil)
	if opts != nil {
		mountProfile = opts.MountProfile
		toolProfiles = opts.ToolMountProfiles
	}
	return mounts.Configure(mount.Config{Profile: mountProfile, SandboxName: instance.Label(), ToolProfiles: toolProfiles})
}

func (s *Service) AddBind(bind mount.Bind) error {
	s.mu.Lock()
	started := s.started
	mounts := s.mounts
	s.mu.Unlock()
	if started {
		return fmt.Errorf("sandbox is already started")
	}
	return mounts.AddBind(bind)
}

func (s *Service) AddMount(req mount.Request) (mount.Entry, error) {
	s.mu.Lock()
	started := s.started
	mounts := s.mounts
	s.mu.Unlock()
	if started {
		return mount.Entry{}, fmt.Errorf("sandbox is already started")
	}
	return mounts.AddMount(req)
}

func (s *Service) Mount(key mount.Key) (mount.Entry, bool) {
	s.mu.Lock()
	mounts := s.mounts
	s.mu.Unlock()
	return mounts.GetMount(key)
}

func (s *Service) StartBinds() []mount.Bind {
	s.mu.Lock()
	s.started = true
	mounts := s.mounts
	s.mu.Unlock()
	return mounts.GetBinds()
}

func (s *Service) RuntimeMounts() []mount.Entry {
	s.mu.Lock()
	mounts := s.mounts
	s.mu.Unlock()
	return mounts.GetMounts()
}

func (s *Service) MountInfos() []mount.Entry {
	s.mu.Lock()
	mounts := s.mounts
	s.mu.Unlock()
	if mounts == nil {
		return nil
	}
	return mounts.GetMounts()
}

func (s *Service) ProjectMounts() []ProjectMount {
	s.mu.Lock()
	defer s.mu.Unlock()
	provider, ok := s.instance.(interface{ ProjectMounts() []ProjectMount })
	if !ok {
		return nil
	}
	return provider.ProjectMounts()
}

func (s *Service) StartBindSnapshot() []mount.Bind {
	s.mu.Lock()
	mounts := s.mounts
	s.mu.Unlock()
	if mounts == nil {
		return nil
	}
	return mounts.GetBinds()
}

func (s *Service) RuntimeInfo(debug bool) RuntimeInfo {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.instance == nil {
		return RuntimeInfo{}
	}
	return s.instance.RuntimeInfo(debug)
}

func (s *Service) MountSetup(ctx context.Context) error {
	s.mu.Lock()
	mounts := s.mounts
	s.mu.Unlock()
	if mounts == nil {
		return nil
	}
	return mounts.RunSetup(ctx, mountRunner{s})
}

func (s *Service) Connect(ctx context.Context, instance Instance, client *host.SandboxClient, exits *CommandExits, managerExit *ManagerExit) error {
	if client == nil {
		return fmt.Errorf("sandbox client is not configured")
	}
	env, err := client.EnvironmentGet(ctx)
	if err != nil {
		return err
	}
	if exits == nil {
		exits = NewCommandExits()
	}
	s.mu.Lock()
	s.instance = instance
	s.client = client
	s.exits = exits
	s.managerExit = managerExit
	s.env = environ.Environment(env).Clone()
	s.started = true
	s.mu.Unlock()
	return nil
}

func (s *Service) SetTobyMCPURL(url string) {
	s.mu.Lock()
	s.mcpURL = url
	s.mu.Unlock()
}

func (s *Service) TobyMCPURL() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.mcpURL
}

func (s *Service) ProjectPath(name string) (string, bool) {
	s.mu.Lock()
	instance := s.instance
	s.mu.Unlock()
	if instance == nil {
		return "", false
	}
	return instance.ProjectPath(name)
}

func (s *Service) VisibleHostPath(repository string) (string, error) {
	s.mu.Lock()
	instance := s.instance
	s.mu.Unlock()
	if instance == nil {
		return "", fmt.Errorf("sandbox is not configured")
	}
	return instance.VisibleHostPath(repository)
}

func (s *Service) GetEnvironment(name string) (string, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	value, ok := s.env[name]
	return value, ok
}

func (s *Service) SetEnvironment(ctx context.Context, name, value string) error {
	name = strings.TrimSpace(name)
	if name == "" || strings.ContainsAny(name, "=\x00") || strings.ContainsRune(value, 0) {
		return fmt.Errorf("invalid environment variable")
	}
	client, _, _, err := s.connected()
	if err != nil {
		return err
	}
	if err := client.EnvironmentSet(ctx, name, value); err != nil {
		return err
	}
	s.mu.Lock()
	if s.env == nil {
		s.env = environ.Environment{}
	}
	if value == "" {
		delete(s.env, name)
	} else {
		s.env[name] = value
	}
	s.mu.Unlock()
	return nil
}

func (s *Service) PrependEnvironment(ctx context.Context, name, value, separator string) error {
	return s.setEnvironmentPathEntry(ctx, name, value, separator, true)
}

func (s *Service) AppendEnvironment(ctx context.Context, name, value, separator string) error {
	return s.setEnvironmentPathEntry(ctx, name, value, separator, false)
}

func (s *Service) setEnvironmentPathEntry(ctx context.Context, name, value, separator string, atStart bool) error {
	if separator == "" {
		separator = ":"
	}
	current, _ := s.GetEnvironment(name)
	parts := strings.Split(current, separator)
	entries := make([]string, 0, len(parts)+1)
	if atStart {
		entries = append(entries, value)
	}
	for _, part := range parts {
		if part == "" || part == value {
			continue
		}
		entries = append(entries, part)
	}
	if !atStart {
		entries = append(entries, value)
	}
	return s.SetEnvironment(ctx, name, strings.Join(entries, separator))
}

func (s *Service) AddFile(ctx context.Context, path string, data []byte, mode uint32) error {
	client, _, _, err := s.connected()
	if err != nil {
		return err
	}
	return client.FileCreate(ctx, path, data, mode)
}

func (s *Service) AddFileOwned(ctx context.Context, path string, data []byte, mode uint32, uid, gid int) error {
	client, _, _, err := s.connected()
	if err != nil {
		return err
	}
	return client.FileCreateOwned(ctx, path, data, mode, uid, gid)
}

func (s *Service) DeletePath(ctx context.Context, path string, recursive bool) error {
	client, _, _, err := s.connected()
	if err != nil {
		return err
	}
	return client.FileDelete(ctx, path, recursive)
}

func (s *Service) Mkdir(ctx context.Context, path string, mode uint32) error {
	client, _, _, err := s.connected()
	if err != nil {
		return err
	}
	return client.FileMkdir(ctx, path, mode)
}

func (s *Service) MkdirOwned(ctx context.Context, path string, mode uint32, uid, gid int) error {
	client, _, _, err := s.connected()
	if err != nil {
		return err
	}
	return client.FileMkdirOwned(ctx, path, mode, uid, gid)
}

func (s *Service) Symlink(ctx context.Context, path, target string) error {
	client, _, _, err := s.connected()
	if err != nil {
		return err
	}
	return client.FileSymlink(ctx, path, target)
}

func (s *Service) SymlinkOwned(ctx context.Context, path, target string, uid, gid int) error {
	client, _, _, err := s.connected()
	if err != nil {
		return err
	}
	return client.FileSymlinkOwned(ctx, path, target, uid, gid)
}

func (s *Service) Exec(ctx context.Context, argv []string, options sandboxapi.ExecOptions) (int, error) {
	client, exits, managerExit, err := s.connected()
	if err != nil {
		return 1, err
	}
	commandID, err := control.NewCommandID()
	if err != nil {
		return 1, err
	}
	exitCh := exits.watch(commandID)
	uid := control.HostUser
	gid := control.HostGroup
	if options.Root {
		uid = 0
		gid = 0
	}
	if err := client.CommandRun(ctx, command.RunParams{CommandID: commandID, Argv: argv, Foreground: options.Foreground, HideOutput: options.HideOutput, UID: uid, GID: gid}); err != nil {
		exits.unwatch(commandID)
		return 1, err
	}
	var managerDone <-chan struct{}
	if managerExit != nil {
		managerDone = managerExit.Done()
	}
	select {
	case result := <-exitCh:
		return commandExitResult(result)
	case <-managerDone:
		result := managerExit.Result()
		if result.Err != nil {
			return 1, result.Err
		}
		return result.ExitCode, fmt.Errorf("sandbox manager exited before command completed")
	case <-ctx.Done():
		exits.unwatch(commandID)
		return 130, ctx.Err()
	}
}

func commandExitResult(result command.ExitParams) (int, error) {
	if result.Error != "" {
		code := result.ExitCode
		if code == 0 {
			code = 1
		}
		return code, exitcode.New(code, "%s", result.Error)
	}
	if result.ExitCode != 0 {
		return result.ExitCode, exitcode.Code(result.ExitCode)
	}
	return result.ExitCode, nil
}

func (s *Service) connected() (*host.SandboxClient, *CommandExits, *ManagerExit, error) {
	s.mu.Lock()
	client := s.client
	exits := s.exits
	managerExit := s.managerExit
	s.mu.Unlock()
	if client == nil || exits == nil {
		return nil, nil, nil, fmt.Errorf("sandbox is not ready")
	}
	return client, exits, managerExit, nil
}

type CommandExits struct {
	mu      sync.Mutex
	waiting map[string]chan command.ExitParams
}

func NewCommandExits() *CommandExits {
	return &CommandExits{waiting: map[string]chan command.ExitParams{}}
}

func (e *CommandExits) watch(commandID string) chan command.ExitParams {
	ch := make(chan command.ExitParams, 1)
	e.mu.Lock()
	e.waiting[commandID] = ch
	e.mu.Unlock()
	return ch
}

func (e *CommandExits) unwatch(commandID string) {
	e.mu.Lock()
	delete(e.waiting, commandID)
	e.mu.Unlock()
}

func (e *CommandExits) Complete(params command.ExitParams) {
	e.mu.Lock()
	ch := e.waiting[params.CommandID]
	delete(e.waiting, params.CommandID)
	e.mu.Unlock()
	if ch != nil {
		ch <- params
	}
}

type ProcessResult struct {
	ExitCode int
	Err      error
}

type ManagerExit struct {
	done chan struct{}
	once sync.Once
	mu   sync.Mutex
	res  ProcessResult
}

func NewManagerExit() *ManagerExit {
	return &ManagerExit{done: make(chan struct{})}
}

func (s *ManagerExit) Set(result ProcessResult) {
	s.mu.Lock()
	s.res = result
	s.mu.Unlock()
	s.once.Do(func() { close(s.done) })
}

func (s *ManagerExit) Done() <-chan struct{} { return s.done }

func (s *ManagerExit) Result() ProcessResult {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.res
}
