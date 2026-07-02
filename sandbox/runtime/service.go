package runtime

// Service implements the tool-facing sandbox.Service for the profile-home topology.
// It is configured per session with the resolved projects and, once the home and
// netns containers are up, wired with the home manager control client (files + exec)
// and the netns-proxied MCP/provider URLs. Filesystem operations and installs run in
// the shared home container via the home manager; the environment is held here on the
// host and passed to each install and serialized into the tool container's launch
// descriptor. Proxy/MCP URLs are the netns proxy's loopback address, reached by the
// tool container through the shared network namespace.

import (
	"context"
	"fmt"
	"strings"
	"sync"

	"petris.dev/toby/container/layout"
	"petris.dev/toby/container/mount"
	"petris.dev/toby/internal/control/host"
	"petris.dev/toby/internal/control/tunnel"
	"petris.dev/toby/platform/environ"
	sandboxapi "petris.dev/toby/sandbox"
)

// InstallSink receives streamed install/exec output (stdout/stderr) so the daemon can
// forward it to the client that started the session.
type InstallSink func(stream string, data []byte)

type Service struct {
	mu sync.Mutex

	projects Projects
	image    string
	env      environ.Environment
	mcpURL   string

	home     *host.SandboxClient
	execUID  int
	execGID  int
	homeDir  string
	homeUp   bool
	sink     InstallSink
	mounts   *mount.Service
	prompter sandboxapi.ApprovalPrompter
}

// SandboxService is the concrete runtime service; the alias keeps call sites that
// reference the tool-facing service by that name working.
type SandboxService = Service

var _ sandboxapi.Service = (*Service)(nil)

func newService(mounts *mount.Service) *Service {
	return &Service{mounts: mounts, homeDir: layout.Home}
}

// Configure resets the service for a new project+profile with the resolved projects
// and image, and points the shared home volume at the profile.
func (s *Service) Configure(spec Spec, profile string) {
	s.mu.Lock()
	s.projects = newProjects(spec.Workdir, spec.Projects)
	s.image = spec.ResolvedImage()
	s.env = environ.Environment{}
	s.mcpURL = ""
	s.home = nil
	s.homeUp = false
	s.prompter = nil
	if s.mounts != nil {
		s.mounts.ConfigureHome(profile)
	}
	s.mu.Unlock()
}

// BindHome wires the home manager control client and seeds the host-held environment
// from the home container's base env; execUID/execGID own installs and the launched
// tool (the invoking user). It marks the service ready for file/exec operations.
func (s *Service) BindHome(home *host.SandboxClient, baseEnv []string, execUID, execGID int) {
	env := environ.Environment{}
	for _, kv := range baseEnv {
		if k, v, ok := strings.Cut(kv, "="); ok {
			env[k] = v
		}
	}
	s.mu.Lock()
	s.home = home
	s.env = env
	s.execUID = execUID
	s.execGID = execGID
	s.homeUp = true
	s.mu.Unlock()
}

// SetInstallSink registers the sink that streamed install output is forwarded to.
func (s *Service) SetInstallSink(sink InstallSink) {
	s.mu.Lock()
	s.sink = sink
	s.mu.Unlock()
}

func (s *Service) SetTobyMCPURL(url string) {
	s.mu.Lock()
	s.mcpURL = url
	s.mu.Unlock()
}

func (s *Service) SetApprovalPrompter(p sandboxapi.ApprovalPrompter) {
	s.mu.Lock()
	s.prompter = p
	s.mu.Unlock()
}

func (s *Service) ApprovalPrompter() sandboxapi.ApprovalPrompter {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.prompter
}

func (s *Service) TobyMCPURL() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.mcpURL
}

// ProxyBaseURL returns the in-sandbox proxied base URL for a registered target id.
func (s *Service) ProxyBaseURL(id string) string {
	return tunnel.ProxyBaseURL(id)
}

func (s *Service) AddBind(bind mount.Bind) error {
	s.mu.Lock()
	mounts := s.mounts
	s.mu.Unlock()
	if mounts == nil {
		return fmt.Errorf("mount service is not configured")
	}
	return mounts.AddBind(bind)
}

func (s *Service) ProjectPath(name string) (string, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.projects.ProjectPath(name)
}

func (s *Service) VisibleHostPath(repository string) (string, error) {
	s.mu.Lock()
	projects := s.projects
	s.mu.Unlock()
	return projects.VisibleHostPath(repository)
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

// Exec runs argv in the shared home container via the home manager and returns the
// exit code. Installs run as the invoking user (root when opts.Root); their output
// streams to the registered install sink unless output is hidden.
func (s *Service) Exec(ctx context.Context, argv []string, opts sandboxapi.ExecOptions) (int, error) {
	client, err := s.readyClient()
	if err != nil {
		return 1, err
	}
	s.mu.Lock()
	uid, gid := s.execUID, s.execGID
	cwd := s.homeDir
	env := s.envSliceLocked()
	sink := s.sink
	s.mu.Unlock()
	if opts.Root {
		uid, gid = 0, 0
	}
	var onChunk host.ChunkFunc
	if !opts.HideOutput && sink != nil {
		onChunk = host.ChunkFunc(sink)
	}
	return client.ExecStream(ctx, argv, env, cwd, uid, gid, onChunk)
}

// LaunchEnv returns the resolved KEY=VALUE environment to serialize into the tool
// container's launch descriptor.
func (s *Service) LaunchEnv() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.envSliceLocked()
}

// ChdirDir is the working directory a launched tool starts in.
func (s *Service) ChdirDir() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.projects.ChdirDir()
}

func (s *Service) ProjectMounts() []ProjectMount {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.projects.ProjectMounts()
}

// RuntimeInfo reports the runtime's self-description for session introspection.
func (s *Service) RuntimeInfo(debug bool) RuntimeInfo {
	s.mu.Lock()
	defer s.mu.Unlock()
	return RuntimeInfo{Runtime: "docker", Info: map[string]any{"image": s.image}}
}

// MountInfos reports the persistent volume mounts for session introspection: the one
// shared home volume for the configured profile.
func (s *Service) MountInfos() []MountInfo {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.mounts == nil {
		return nil
	}
	return []MountInfo{{
		Key:     "runtime.home",
		Profile: s.mounts.Profile(),
		Volume:  s.mounts.HomeVolume(),
		Target:  s.homeDir,
		Access:  string(mount.AccessRegular),
	}}
}

// StartBindSnapshot reports the registered host binds for session introspection.
func (s *Service) StartBindSnapshot() []mount.Bind {
	s.mu.Lock()
	mounts := s.mounts
	s.mu.Unlock()
	if mounts == nil {
		return nil
	}
	return mounts.Binds()
}

// Paths methods (sandbox.Paths).
func (s *Service) HomeDir() string  { return s.projectsValue().HomeDir() }
func (s *Service) Projects() string { return s.projectsValue().Projects() }
func (s *Service) TobyRuntimeDir() string {
	return s.projectsValue().TobyRuntimeDir()
}

func (s *Service) projectsValue() Projects {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.projects
}

func (s *Service) readyClient() (*host.SandboxClient, error) {
	s.mu.Lock()
	client := s.home
	up := s.homeUp
	s.mu.Unlock()
	if client == nil || !up {
		return nil, fmt.Errorf("home manager is not ready")
	}
	return client, nil
}

func (s *Service) envSliceLocked() []string {
	out := make([]string, 0, len(s.env))
	for k, v := range s.env {
		out = append(out, k+"="+v)
	}
	return out
}
