package runtime

// Service implements the tool-facing sandbox.Service. Before the sandbox starts it
// accumulates mounts and binds; after BindRun it brokers file, env, and command
// operations against the live container — files via docker cp, commands via docker
// exec — with the environment held here on the host and injected into each exec.

import (
	"context"
	"fmt"
	"os"
	"strings"
	"sync"

	"petris.dev/toby/container/mount"
	"petris.dev/toby/internal/control/tunnel"
	"petris.dev/toby/platform/environ"
	sandboxapi "petris.dev/toby/sandbox"
)

type Service struct {
	mu       sync.Mutex
	instance Instance
	env      environ.Environment
	mcpURL   string
	mounts   *mount.Service
	started  bool
}

type SandboxService = Service

func newService(mounts *mount.Service) *Service { return &Service{mounts: mounts} }

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
	inst, err := s.ready()
	if err != nil {
		return err
	}
	return inst.WriteFile(ctx, path, data, mode, 0, 0)
}

func (s *Service) AddFileOwned(ctx context.Context, path string, data []byte, mode uint32, uid, gid int) error {
	inst, err := s.ready()
	if err != nil {
		return err
	}
	return inst.WriteFile(ctx, path, data, mode, uid, gid)
}

func (s *Service) DeletePath(ctx context.Context, path string, recursive bool) error {
	inst, err := s.ready()
	if err != nil {
		return err
	}
	return inst.DeletePath(ctx, path, recursive)
}

func (s *Service) Mkdir(ctx context.Context, path string, mode uint32) error {
	inst, err := s.ready()
	if err != nil {
		return err
	}
	return inst.MakeDir(ctx, path, mode, 0, 0)
}

func (s *Service) MkdirOwned(ctx context.Context, path string, mode uint32, uid, gid int) error {
	inst, err := s.ready()
	if err != nil {
		return err
	}
	return inst.MakeDir(ctx, path, mode, uid, gid)
}

func (s *Service) Symlink(ctx context.Context, path, target string) error {
	inst, err := s.ready()
	if err != nil {
		return err
	}
	return inst.MakeSymlink(ctx, path, target, 0, 0)
}

func (s *Service) SymlinkOwned(ctx context.Context, path, target string, uid, gid int) error {
	inst, err := s.ready()
	if err != nil {
		return err
	}
	return inst.MakeSymlink(ctx, path, target, uid, gid)
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
	return inst.Exec(ctx, ExecSpec{
		Argv:        argv,
		Env:         s.envSlice(),
		User:        user,
		Interactive: options.Foreground,
		HideOutput:  options.HideOutput,
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

func (s *Service) envSlice() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]string, 0, len(s.env))
	for k, v := range s.env {
		out = append(out, k+"="+v)
	}
	return out
}
