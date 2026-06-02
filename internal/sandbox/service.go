package sandbox

import (
	"context"
	"fmt"
	"os"
	"strings"
	"sync"

	"petris.dev/toby/internal/control"
	"petris.dev/toby/internal/control/hostmanager"
	"petris.dev/toby/internal/diagnostic/exitcode"
	sandboxmount "petris.dev/toby/internal/sandbox/mount"
	sandboxpath "petris.dev/toby/internal/sandbox/path"
	"petris.dev/toby/internal/tools/tool"
)

type Service struct {
	mu          sync.Mutex
	instance    Instance
	client      *hostmanager.SandboxClient
	exits       *CommandExits
	managerExit *ManagerExit
	env         tool.Environment
	mcpURL      string
	binds       []sandboxmount.Bind
	seenBinds   map[sandboxmount.Bind]bool
	mounts      *sandboxmount.Service
	started     bool
}

type SandboxService = Service

func newService(mounts *sandboxmount.Service) *Service { return &Service{mounts: mounts} }

var _ tool.SandboxService = (*Service)(nil)

func (s *Service) Prepare(instance Instance) {
	s.mu.Lock()
	s.instance = instance
	s.client = nil
	s.exits = nil
	s.managerExit = nil
	s.env = nil
	s.mcpURL = ""
	s.binds = nil
	s.seenBinds = nil
	s.started = false
	s.mu.Unlock()
}

func (s *Service) ConfigureMounts(opts *tool.CommandOptions) error {
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
	profiles := sandboxmount.Profiles{}
	mountProfile := ""
	toolProfiles := map[string]string(nil)
	if opts != nil {
		profiles = opts.MountProfiles
		mountProfile = opts.MountProfile
		toolProfiles = opts.ToolMountProfiles
	}
	return mounts.Configure(sandboxmount.Config{Profile: mountProfile, SandboxName: instance.Label(), Paths: instance.Paths(), Profiles: profiles, ToolProfiles: toolProfiles})
}

func (s *Service) AddBind(bind sandboxmount.Bind) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.started {
		return fmt.Errorf("sandbox is already started")
	}
	if s.seenBinds == nil {
		s.seenBinds = map[sandboxmount.Bind]bool{}
	}
	if s.seenBinds[bind] {
		return nil
	}
	s.seenBinds[bind] = true
	s.binds = append(s.binds, bind)
	return nil
}

func (s *Service) AddMount(req sandboxmount.Request) (sandboxmount.Info, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.started {
		return sandboxmount.Info{}, fmt.Errorf("sandbox is already started")
	}
	return s.mounts.Add(req)
}

func (s *Service) Mount(key sandboxmount.Key) (sandboxmount.Info, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.mounts.Get(key)
}

func (s *Service) StartBinds() []sandboxmount.Bind {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.started = true
	return append([]sandboxmount.Bind(nil), s.binds...)
}

func (s *Service) RuntimeMounts() []sandboxmount.RuntimeMount {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.mounts.RuntimeMounts()
}

func (s *Service) HostBackedManagedMounts() []sandboxmount.Info {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.mounts.HostBackedManagedMounts()
}

func (s *Service) MountSetup(ctx context.Context) error {
	mounts := s.RuntimeMounts()
	argv := []string{"chown", "-R", fmt.Sprintf("%d:%d", os.Getuid(), os.Getgid())}
	for _, item := range mounts {
		if item.Source.Kind == sandboxmount.SourceProvider && item.SetupPath != "" {
			argv = append(argv, item.SetupPath)
		}
	}
	if len(argv) == 3 {
		return nil
	}
	_, err := s.Exec(ctx, argv, tool.ExecOptions{Root: true, HideOutput: true})
	return err
}

func (s *Service) Connect(ctx context.Context, instance Instance, client *hostmanager.SandboxClient, exits *CommandExits, managerExit *ManagerExit) error {
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
	s.env = tool.Environment(env).Clone()
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

func (s *Service) Paths() sandboxpath.Paths {
	s.mu.Lock()
	instance := s.instance
	s.mu.Unlock()
	if instance == nil {
		return sandboxpath.Paths{}
	}
	return instance.Paths()
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
		s.env = tool.Environment{}
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

func (s *Service) Exec(ctx context.Context, argv []string, options tool.ExecOptions) (int, error) {
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
	if err := client.CommandRun(ctx, control.CommandRunParams{CommandID: commandID, Argv: argv, Foreground: options.Foreground, HideOutput: options.HideOutput, UID: uid, GID: gid}); err != nil {
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

func commandExitResult(result control.CommandExitParams) (int, error) {
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

func (s *Service) connected() (*hostmanager.SandboxClient, *CommandExits, *ManagerExit, error) {
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
	waiting map[string]chan control.CommandExitParams
}

func NewCommandExits() *CommandExits {
	return &CommandExits{waiting: map[string]chan control.CommandExitParams{}}
}

func (e *CommandExits) watch(commandID string) chan control.CommandExitParams {
	ch := make(chan control.CommandExitParams, 1)
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

func (e *CommandExits) Complete(params control.CommandExitParams) {
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
