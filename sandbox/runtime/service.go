package runtime

// Service implements the tool-facing sandbox.Service. Before the sandbox starts it
// accumulates mounts and binds; after BindRun it brokers file, env, and command
// operations against the live container — files through the sandbox manager control
// session, commands via docker exec — with the environment held here on the host
// and injected into each exec.

import (
	"context"
	"fmt"
	"os"
	"strings"
	"sync"

	"petris.dev/toby/container/mount"
	"petris.dev/toby/internal/control/host"
	"petris.dev/toby/internal/control/tunnel"
	"petris.dev/toby/platform/environ"
	sandboxapi "petris.dev/toby/sandbox"
)

type Service struct {
	mu               sync.Mutex
	instance         Instance
	env              environ.Environment
	mcpURL           string
	client           *host.SandboxClient
	mounts           *mount.Service
	started          bool
	managedTerminal  bool
	approvalPrompter sandboxapi.ApprovalPrompter
}

type SandboxService = Service

// newService starts with the managed terminal on; the session toggles it from config.
func newService(mounts *mount.Service) *Service {
	return &Service{mounts: mounts, managedTerminal: true}
}

// mountRunner adapts the sandbox service to mount.Executor for setup hooks.
type mountRunner struct{ s *Service }

var _ mount.Executor = mountRunner{}

func (r mountRunner) Exec(ctx context.Context, argv []string, root bool) (int, error) {
	return r.s.Exec(ctx, argv, sandboxapi.ExecOptions{Root: root, HideOutput: true})
}

var _ sandboxapi.Service = (*Service)(nil)

func (s *Service) Prepare(instance Instance) {
	s.mu.Lock()
	s.instance = instance
	s.env = nil
	s.mcpURL = ""
	s.client = nil
	s.started = false
	s.mu.Unlock()
}

func (s *Service) ConfigureMounts(mountProfile string, toolProfiles map[string]string) error {
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
	return mounts.Mount(key)
}

func (s *Service) StartBinds() []mount.Bind {
	s.mu.Lock()
	s.started = true
	mounts := s.mounts
	s.mu.Unlock()
	return mounts.Binds()
}

func (s *Service) RuntimeMounts() []mount.Entry {
	s.mu.Lock()
	mounts := s.mounts
	s.mu.Unlock()
	return mounts.Mounts()
}

func (s *Service) MountInfos() []mount.Entry {
	s.mu.Lock()
	mounts := s.mounts
	s.mu.Unlock()
	if mounts == nil {
		return nil
	}
	return mounts.Mounts()
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
	return mounts.Binds()
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

// BindRun marks the service ready against the started container and seeds the
// host-held environment from the container's base env plus any extra overrides.
func (s *Service) BindRun(ctx context.Context, instance Instance, extra environ.Environment) error {
	if instance == nil {
		return fmt.Errorf("sandbox instance is not configured")
	}
	base, err := instance.RunContainerEnv(ctx)
	if err != nil {
		return err
	}
	env := environ.Environment{}
	for _, kv := range base {
		if k, v, ok := strings.Cut(kv, "="); ok {
			env[k] = v
		}
	}
	for k, v := range extra {
		env[k] = v
	}
	s.mu.Lock()
	s.instance = instance
	s.env = env
	s.started = true
	s.mu.Unlock()
	return nil
}

// ProxyBaseURL returns the in-sandbox proxied base URL for a registered target id.
func (s *Service) ProxyBaseURL(id string) string {
	return tunnel.ProxyBaseURL(id)
}

// SetManagedTerminal selects whether an interactive foreground tool runs under Toby's
// managed terminal (raw-passthrough shadow plus the approval modal) or a plain raw
// passthrough. Set once per session from config.
func (s *Service) SetManagedTerminal(enabled bool) {
	s.mu.Lock()
	s.managedTerminal = enabled
	s.mu.Unlock()
}

// SetApprovalPrompter registers (or clears, with nil) the prompter that shows the
// approval modal. The interactive foreground sets it while it owns the terminal.
func (s *Service) SetApprovalPrompter(p sandboxapi.ApprovalPrompter) {
	s.mu.Lock()
	s.approvalPrompter = p
	s.mu.Unlock()
}

// ApprovalPrompter returns the active approval prompter, or nil when none is running.
func (s *Service) ApprovalPrompter() sandboxapi.ApprovalPrompter {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.approvalPrompter
}

func (s *Service) SetTobyMCPURL(url string) {
	s.mu.Lock()
	s.mcpURL = url
	s.mu.Unlock()
}

func (s *Service) SetSandboxClient(client *host.SandboxClient) {
	s.mu.Lock()
	s.client = client
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

func (s *Service) Environment(name string) (string, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	value, ok := s.env[name]
	return value, ok
}

func (s *Service) SetEnvironment(_ context.Context, name, value string) error {
	name = strings.TrimSpace(name)
	if name == "" || strings.ContainsAny(name, "=\x00") || strings.ContainsRune(value, 0) {
		return fmt.Errorf("invalid environment variable")
	}
	if _, err := s.ready(); err != nil {
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
	current, _ := s.Environment(name)
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
	client, err := s.readyClient()
	if err != nil {
		return err
	}
	return client.FileCreate(ctx, path, data, mode)
}

func (s *Service) AddFileOwned(ctx context.Context, path string, data []byte, mode uint32, uid, gid int) error {
	client, err := s.readyClient()
	if err != nil {
		return err
	}
	return client.FileCreateOwned(ctx, path, data, mode, uid, gid)
}

func (s *Service) DeletePath(ctx context.Context, path string, recursive bool) error {
	client, err := s.readyClient()
	if err != nil {
		return err
	}
	return client.FileDelete(ctx, path, recursive)
}

func (s *Service) Mkdir(ctx context.Context, path string, mode uint32) error {
	client, err := s.readyClient()
	if err != nil {
		return err
	}
	return client.FileMkdir(ctx, path, mode)
}

func (s *Service) MkdirOwned(ctx context.Context, path string, mode uint32, uid, gid int) error {
	client, err := s.readyClient()
	if err != nil {
		return err
	}
	return client.FileMkdirOwned(ctx, path, mode, uid, gid)
}

func (s *Service) Symlink(ctx context.Context, path, target string) error {
	client, err := s.readyClient()
	if err != nil {
		return err
	}
	return client.FileSymlink(ctx, path, target)
}

func (s *Service) SymlinkOwned(ctx context.Context, path, target string, uid, gid int) error {
	client, err := s.readyClient()
	if err != nil {
		return err
	}
	return client.FileSymlinkOwned(ctx, path, target, uid, gid)
}

func (s *Service) Exec(ctx context.Context, argv []string, options sandboxapi.ExecOptions) (int, error) {
	inst, err := s.ready()
	if err != nil {
		return 1, err
	}
	user := fmt.Sprintf("%d:%d", os.Getuid(), os.Getgid())
	if options.Root {
		user = "0:0"
	}
	s.mu.Lock()
	managed := s.managedTerminal
	s.mu.Unlock()
	return inst.Exec(ctx, ExecSpec{
		Argv:             argv,
		Env:              s.envSlice(),
		User:             user,
		Interactive:      options.Foreground,
		HideOutput:       options.HideOutput,
		Managed:          managed,
		RegisterPrompter: s.SetApprovalPrompter,
	})
}

func (s *Service) ready() (Instance, error) {
	s.mu.Lock()
	inst := s.instance
	started := s.started
	s.mu.Unlock()
	if inst == nil || !started {
		return nil, fmt.Errorf("sandbox is not ready")
	}
	return inst, nil
}

func (s *Service) readyClient() (*host.SandboxClient, error) {
	s.mu.Lock()
	client := s.client
	started := s.started
	s.mu.Unlock()
	if client == nil || !started {
		return nil, fmt.Errorf("sandbox is not ready")
	}
	return client, nil
}

func (s *Service) envSlice() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]string, 0, len(s.env))
	for k, v := range s.env {
		out = append(out, k+"="+v)
	}
	return out
}
