package sandbox

import (
	"context"
	"errors"
	"fmt"
	"os"
	pathpkg "path"
	"path/filepath"
	"strings"
	"sync"

	"petris.dev/toby/fusekit"
	"petris.dev/toby/internal/config"
	"petris.dev/toby/internal/executil"
	"petris.dev/toby/internal/exitcode"
	"petris.dev/toby/internal/tool"
)

type Factory struct {
	paths  config.Paths
	runner executil.Runner
}

func NewFactory(paths config.Paths, runner executil.Runner) Factory {
	return Factory{paths: paths, runner: runner}
}

type Sandbox struct {
	paths             config.Paths
	runner            executil.Runner
	label             string
	homeDir           string
	projectDir        string
	sandboxProjDir    string
	tempHome          string
	mountableProjects bool
}

type bwrapBind struct {
	HostPath    string
	SandboxPath string
	Type        tool.BindType
	Optional    bool
}

type CommandMounts struct {
	RuntimeMountpoint string
	Binds             []bwrapBind
}

type HomeFS struct {
	Mountpoint  string
	Binds       []bwrapBind
	server      *fusekit.Server
	router      *fusekit.Router
	baseMounts  []fusekit.Mount
	dynamic     []fusekit.Mount
	overlays    []fusekit.Mount
	visible     []visibleProject
	projectRoot string
	mu          sync.Mutex
	nextID      int
}

type visibleProject struct {
	Base   string
	Source string
}

func (h *HomeFS) CommandMounts() CommandMounts {
	if h == nil {
		return CommandMounts{}
	}
	return CommandMounts{RuntimeMountpoint: h.Mountpoint, Binds: append([]bwrapBind(nil), h.Binds...)}
}

func (s *Sandbox) CommandMountsWithoutFUSE(toolset *tool.Toolset) CommandMounts {
	return CommandMounts{Binds: bwrapBindsForToolset(toolset)}
}

func (h *HomeFS) Close() error {
	if h == nil {
		return nil
	}
	var err error
	if h.server != nil {
		err = h.server.Unmount()
		h.server = nil
	}
	if removeErr := os.RemoveAll(h.Mountpoint); err == nil {
		err = removeErr
	}
	return err
}

func (h *HomeFS) AddOverlayMount(mount fusekit.Mount) error {
	if h == nil || h.router == nil {
		return errors.New("home filesystem is not running")
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	h.overlays = append(h.overlays, mount)
	return h.replaceLocked()
}

func (h *HomeFS) AddHostPath(path string) (string, error) {
	if h == nil || h.router == nil {
		return "", errors.New("home filesystem is not running")
	}
	virtual, source, err := h.validateProjectPath(path)
	if err != nil {
		return "", err
	}

	h.mu.Lock()
	defer h.mu.Unlock()
	for _, mount := range append(append([]fusekit.Mount(nil), h.baseMounts...), h.dynamic...) {
		if mount.BasePath() == virtual {
			return virtual, nil
		}
	}
	mount, err := fusekit.NewPassthroughMount(fusekit.PassthroughOptions{
		ID:       fmt.Sprintf("dynamic-%d", h.nextID),
		BasePath: virtual,
		Source:   source,
	})
	if err != nil {
		return "", err
	}
	h.nextID++
	h.dynamic = append(h.dynamic, mount)
	if err := h.replaceLocked(); err != nil {
		return "", err
	}
	return virtual, nil
}

func (h *HomeFS) VisibleHostPath(repository string) (string, error) {
	if h == nil || h.router == nil {
		return "", errors.New("home filesystem is not running")
	}
	virtual, err := repositoryVirtualPath(repository)
	if err != nil {
		return "", err
	}

	h.mu.Lock()
	mounts := append(append([]fusekit.Mount(nil), h.baseMounts...), h.dynamic...)
	visible := append([]visibleProject(nil), h.visible...)
	h.mu.Unlock()

	var best visibleProject
	bestBase := ""
	for _, project := range visible {
		if !virtualPathWithin(project.Base, virtual) || len(project.Base) <= len(bestBase) {
			continue
		}
		best = project
		bestBase = project.Base
	}
	for _, mount := range mounts {
		passthrough, ok := mount.(*fusekit.PassthroughMount)
		if !ok {
			continue
		}
		base := passthrough.BasePath()
		if base != "/projects" && !strings.HasPrefix(base, "/projects/") {
			continue
		}
		if !virtualPathWithin(base, virtual) || len(base) <= len(bestBase) {
			continue
		}
		best = visibleProject{Base: base, Source: passthrough.Source()}
		bestBase = base
	}
	if bestBase == "" {
		return "", fmt.Errorf("repository is not visible: %s", repository)
	}
	rel := strings.TrimPrefix(virtual, bestBase)
	rel = strings.TrimPrefix(rel, "/")
	hostPath := best.Source
	if rel != "" {
		hostPath = filepath.Join(hostPath, filepath.FromSlash(rel))
	}
	return validateVisibleHostPath(best.Source, hostPath)
}

func (h *HomeFS) validateProjectPath(path string) (string, string, error) {
	if path == "" {
		return "", "", fmt.Errorf("path is required")
	}
	if !filepath.IsAbs(path) {
		return "", "", fmt.Errorf("path must be absolute: %s", path)
	}
	cleaned := filepath.Clean(path)
	virtual, err := projectVirtualPath(h.projectRoot, cleaned)
	if err != nil {
		return "", "", err
	}
	resolved, err := filepath.EvalSymlinks(cleaned)
	if err != nil {
		return "", "", err
	}
	root := h.projectRoot
	if resolvedRoot, err := filepath.EvalSymlinks(h.projectRoot); err == nil {
		root = resolvedRoot
	}
	if _, err := relativeTo(root, resolved); err != nil {
		return "", "", fmt.Errorf("resolved path is outside %s: %w", h.projectRoot, err)
	}
	return virtual, resolved, nil
}

func (h *HomeFS) replaceLocked() error {
	mounts := make([]fusekit.Mount, 0, len(h.baseMounts)+len(h.dynamic)+len(h.overlays))
	mounts = append(mounts, h.baseMounts...)
	mounts = append(mounts, h.dynamic...)
	mounts = append(mounts, h.overlays...)
	return h.router.Replace(mounts)
}

func (f Factory) FromOptions(opts *tool.CommandOptions) (*Sandbox, error) {
	if opts.TmpEnv {
		return f.fromTemporaryEnvironment(opts)
	}
	return f.fromPersistentEnvironment(opts)
}

func (f Factory) fromTemporaryEnvironment(opts *tool.CommandOptions) (*Sandbox, error) {
	if opts.Project != "" && opts.Env != "" {
		return nil, exitcode.New(2, "tmp env project specified twice: use either positional PROJECT or --project DIR")
	}
	if opts.Env == "" && opts.Project == "" {
		return nil, exitcode.New(2, "tmp env requires a project")
	}

	tempHome, err := os.MkdirTemp("", "toby-")
	if err != nil {
		return nil, err
	}
	projectName := opts.Project
	if projectName == "" {
		projectName = opts.Env
	}
	projectDir, err := f.resolveProjectDir("", projectName, true)
	if err != nil {
		_ = os.RemoveAll(tempHome)
		return nil, err
	}
	sandboxProjDir := projectDir
	if sandboxProjDir == "" {
		sandboxProjDir = filepath.Join(f.paths.ProjectRoot, filepath.Base(tempHome))
	}
	return &Sandbox{
		paths:             f.paths,
		runner:            f.runner,
		label:             "tmp",
		homeDir:           tempHome,
		projectDir:        projectDir,
		sandboxProjDir:    sandboxProjDir,
		tempHome:          tempHome,
		mountableProjects: opts.MountableProjects,
	}, nil
}

func (f Factory) fromPersistentEnvironment(opts *tool.CommandOptions) (*Sandbox, error) {
	if opts.Env == "" {
		return nil, exitcode.New(2, "environment name is required unless --tmp-env is used")
	}
	if err := os.MkdirAll(f.paths.SandboxRoot, 0o755); err != nil {
		return nil, err
	}
	projectDir, err := f.resolveProjectDir(opts.Env, opts.Project, false)
	if err != nil {
		return nil, err
	}
	sandboxProjDir := projectDir
	if sandboxProjDir == "" {
		sandboxProjDir = filepath.Join(f.paths.ProjectRoot, opts.Env)
	}
	return &Sandbox{
		paths:             f.paths,
		runner:            f.runner,
		label:             opts.Env,
		homeDir:           filepath.Join(f.paths.SandboxRoot, opts.Env),
		projectDir:        projectDir,
		sandboxProjDir:    sandboxProjDir,
		mountableProjects: opts.MountableProjects,
	}, nil
}

func (f Factory) resolveProjectDir(envName, project string, tmpEnv bool) (string, error) {
	var raw string
	switch {
	case project == "":
		if envName == "" {
			return "", nil
		}
		raw = filepath.Join(f.paths.ProjectRoot, envName)
	case tmpEnv && isProjectShorthand(project):
		raw = filepath.Join(f.paths.ProjectRoot, project)
	default:
		raw = config.ExpandHome(project, f.paths.Home)
	}
	abs, err := filepath.Abs(raw)
	if err != nil {
		return "", err
	}
	info, err := os.Stat(abs)
	if err != nil || !info.IsDir() {
		return "", exitcode.New(1, "failed to resolve project directory: %s does not exist", raw)
	}
	if _, err := relativeTo(f.paths.ProjectRoot, abs); err != nil {
		return "", exitcode.New(1, "project directory must be under %s: %s", f.paths.ProjectRoot, err)
	}
	return abs, nil
}

func isProjectShorthand(project string) bool {
	if project == "." || project == ".." || filepath.IsAbs(project) {
		return false
	}
	return !strings.ContainsRune(project, os.PathSeparator)
}

func (s *Sandbox) HomeDir() string { return s.homeDir }

func (s *Sandbox) ProjectRoot() string { return s.paths.ProjectRoot }

func (s *Sandbox) OpenCodeConfigDir() string {
	return filepath.Join(s.paths.SandboxRoot, ".config", "opencode")
}

func (s *Sandbox) StateHomeDir() string {
	if s.paths.StateHome != "" {
		return s.paths.StateHome
	}
	return filepath.Join(s.paths.Home, ".local", "state")
}

func (s *Sandbox) TobyStateDir() string {
	return filepath.Join(s.StateHomeDir(), "toby")
}

func (s *Sandbox) TobyStaticDir() string {
	return filepath.Join(s.TobyStateDir(), "static")
}

func (s *Sandbox) TobyMountBasePath() (string, error) {
	return "/toby", nil
}

func (s *Sandbox) TobyGitAgentsPath() string {
	return filepath.Join(s.TobyStaticDir(), "GIT_AGENTS.md")
}

func (s *Sandbox) TobyProjectMountAgentsPath() string {
	return filepath.Join(s.TobyStaticDir(), "PROJECT_MOUNT_AGENTS.md")
}

func (s *Sandbox) TobyOpenCodeConfigDir() string {
	return filepath.Join(s.TobyStaticDir(), "opencode")
}

func (s *Sandbox) Cleanup() error {
	if s.tempHome == "" {
		return nil
	}
	tempHome := s.tempHome
	s.tempHome = ""
	return os.RemoveAll(tempHome)
}

func (s *Sandbox) EnsureHome() error {
	return os.MkdirAll(s.homeDir, 0o755)
}

func (s *Sandbox) EnsureSandboxProjectDir() error {
	if s.projectDir != "" {
		if s.mountableProjects {
			return nil
		}
		source, err := s.sandboxProjectSourceDir()
		if err != nil {
			return nil
		}
		return os.MkdirAll(source, 0o755)
	}
	source, err := s.sandboxProjectSourceDir()
	if err != nil {
		return exitcode.New(1, "failed to create sandbox project directory: %s; set XDG_PROJECTS_DIR inside %s or provide --project", err, s.paths.Home)
	}
	return os.MkdirAll(source, 0o755)
}

func (s *Sandbox) sandboxProjectSourceDir() (string, error) {
	rel, err := relativeTo(s.paths.Home, s.sandboxProjDir)
	if err != nil {
		return "", err
	}
	return filepath.Join(s.homeDir, rel), nil
}

func relativeTo(base, path string) (string, error) {
	absBase, err := filepath.Abs(base)
	if err != nil {
		return "", err
	}
	absPath, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	rel, err := filepath.Rel(absBase, absPath)
	if err != nil {
		return "", err
	}
	if rel == ".." || strings.HasPrefix(rel, ".."+string(os.PathSeparator)) {
		return "", fmt.Errorf("%s must be equal to or inside %s", absPath, absBase)
	}
	return rel, nil
}

func (s *Sandbox) SetupContext(ctx *tool.RunContext) {
	ctx.Env["XDG_STATE_HOME"] = s.StateHomeDir()
	ctx.Env["GRML_CHROOT"] = "1"
	ctx.Env["CHROOT"] = "(" + s.label + ")"
	ctx.Env["BASH_ENV"] = filepath.Join(s.paths.Home, ".env")
	if s.mountableProjects {
		ctx.Env["TOBY_MOUNTABLE_PROJECTS"] = "1"
	} else {
		delete(ctx.Env, "TOBY_MOUNTABLE_PROJECTS")
	}
	ctx.Env.Prepend("PATH", filepath.Join(s.paths.Home, ".local", "bin"))
	entries := ctx.Toolset.PathEntries()
	for i := len(entries) - 1; i >= 0; i-- {
		ctx.Env.Prepend("PATH", entries[i])
	}
	ctx.Env.Prepend("PATH", filepath.Join(s.TobyStaticDir(), "bin"))
}

func (s *Sandbox) BuildCommand(argv []string, mounts CommandMounts) []string {
	args := []string{
		"/usr/bin/bwrap",
		"--die-with-parent",
		"--unshare-pid",
		"--proc", "/proc",
		"--dev-bind", "/dev", "/dev",
		"--tmpfs", "/tmp",
		"--ro-bind-try", "/etc", "/etc",
		"--ro-bind-try", "/opt", "/opt",
		"--bind-try", "/sys", "/sys",
		"--ro-bind-try", "/usr", "/usr",
		"--symlink", "usr/bin", "/bin",
		"--symlink", "usr/bin", "/sbin",
		"--symlink", "usr/lib", "/lib",
		"--symlink", "usr/lib", "/lib64",
		"--ro-bind-try", "/var/empty", "/var/empty",
		"--tmpfs", s.paths.XDGRuntimeDir,
		"--ro-bind-try", "/run/systemd/resolve", "/run/systemd/resolve",
	}
	args = append(args, s.runtimeBind(filepath.Join(s.paths.XDGRuntimeDir, "pulse"))...)
	args = append(args, s.runtimeBind(filepath.Join(s.paths.XDGRuntimeDir, s.paths.PipewireCore))...)
	args = append(args, "--ro-bind-try", "/run/udev", "/run/udev")
	args = append(args, s.runtimeBind(filepath.Join(s.paths.XDGRuntimeDir, s.paths.WaylandDisplay))...)
	args = append(args,
		"--ro-bind-try", "/tmp/.ICE-unix", "/tmp/.ICE-unix",
		"--ro-bind-try", "/tmp/.X11-unix", "/tmp/.X11-unix",
	)
	if s.paths.XAuthority != "" {
		args = append(args, "--ro-bind-try", s.paths.XAuthority, s.paths.XAuthority)
	}
	args = append(args,
		"--bind", s.homeDir, s.paths.Home,
		"--bind", "/usr/bin/true", "/usr/bin/xdg-open",
	)
	if mounts.RuntimeMountpoint != "" {
		args = append(args,
			"--bind", filepath.Join(mounts.RuntimeMountpoint, "toby"), s.TobyStateDir(),
		)
	}
	if s.mountableProjects && mounts.RuntimeMountpoint != "" {
		args = append(args, "--bind", filepath.Join(mounts.RuntimeMountpoint, "projects"), s.paths.ProjectRoot)
	} else if s.projectDir != "" {
		args = append(args, "--bind", s.projectDir, s.sandboxProjDir)
	}
	for _, bind := range mounts.Binds {
		args = append(args, bindFlag(bind.Type, bind.Optional), bind.HostPath, bind.SandboxPath)
	}
	args = append(args, "--chdir", s.sandboxProjDir)
	args = append(args, argv...)
	return args
}

func (s *Sandbox) StartHomeFS(ctx context.Context, toolset *tool.Toolset) (*HomeFS, error) {
	homeMounts, bwrapBinds, visible, err := s.buildHomeMounts(toolset)
	if err != nil {
		return nil, err
	}
	router, err := fusekit.NewRouter(homeMounts)
	if err != nil {
		return nil, err
	}
	mountpoint, err := os.MkdirTemp("", "toby-home-fuse-")
	if err != nil {
		return nil, err
	}
	server, err := fusekit.MountServer(ctx, mountpoint, router)
	if err != nil {
		_ = os.RemoveAll(mountpoint)
		return nil, fmt.Errorf("failed to mount HOME FUSE filesystem at %s: %w", mountpoint, err)
	}
	return &HomeFS{
		Mountpoint:  mountpoint,
		Binds:       bwrapBinds,
		server:      server,
		router:      router,
		baseMounts:  append([]fusekit.Mount(nil), homeMounts...),
		visible:     append([]visibleProject(nil), visible...),
		projectRoot: s.paths.ProjectRoot,
	}, nil
}

func (s *Sandbox) buildHomeMounts(toolset *tool.Toolset) ([]fusekit.Mount, []bwrapBind, []visibleProject, error) {
	root, err := fusekit.NewEmptyDirMount("runtime-root", "/", 0o500)
	if err != nil {
		return nil, nil, nil, err
	}
	homeMounts := []fusekit.Mount{root}
	bwrapBinds := bwrapBindsForToolset(toolset)
	var visible []visibleProject
	if s.mountableProjects {
		projects, err := fusekit.NewEmptyDirMount("projects-root", "/projects", 0o500)
		if err != nil {
			return nil, nil, nil, err
		}
		homeMounts = append(homeMounts, projects)
	}
	if s.sandboxProjDir != "" && s.mountableProjects {
		base, err := projectVirtualPath(s.paths.ProjectRoot, s.sandboxProjDir)
		if err != nil {
			return nil, nil, nil, exitcode.New(1, "project directory must be under %s: %s", s.paths.ProjectRoot, err)
		}
		source := s.projectDir
		if source == "" {
			source, err = s.sandboxProjectSourceDir()
			if err != nil {
				return nil, nil, nil, err
			}
		}
		project, err := fusekit.NewPassthroughMount(fusekit.PassthroughOptions{
			ID:       "project",
			BasePath: base,
			Source:   source,
		})
		if err != nil {
			return nil, nil, nil, err
		}
		homeMounts = append(homeMounts, project)
		visible = append(visible, visibleProject{Base: base, Source: source})
	} else if s.projectDir != "" {
		base, err := projectVirtualPath(s.paths.ProjectRoot, s.sandboxProjDir)
		if err != nil {
			return nil, nil, nil, exitcode.New(1, "project directory must be under %s: %s", s.paths.ProjectRoot, err)
		}
		visible = append(visible, visibleProject{Base: base, Source: s.projectDir})
	}
	return homeMounts, bwrapBinds, visible, nil
}

func bwrapBindsForToolset(toolset *tool.Toolset) []bwrapBind {
	if toolset == nil {
		return nil
	}
	binds := toolset.Binds()
	result := make([]bwrapBind, 0, len(binds))
	for _, bind := range binds {
		result = append(result, bwrapBind(bind))
	}
	return result
}

func projectVirtualPath(projectRoot, path string) (string, error) {
	rel, err := relativeTo(projectRoot, path)
	if err != nil {
		return "", err
	}
	if rel == "." {
		return "/projects", nil
	}
	return "/projects/" + filepath.ToSlash(rel), nil
}

func repositoryVirtualPath(repository string) (string, error) {
	repository = strings.TrimSpace(repository)
	if repository == "" || pathpkg.IsAbs(repository) || strings.ContainsRune(repository, 0) {
		return "", fmt.Errorf("invalid repository name")
	}
	segments := strings.Split(repository, "/")
	for _, segment := range segments {
		if segment == "" || segment == "." || segment == ".." {
			return "", fmt.Errorf("invalid repository name")
		}
	}
	return "/projects/" + strings.Join(segments, "/"), nil
}

func virtualPathWithin(base, path string) bool {
	return path == base || strings.HasPrefix(path, base+"/")
}

func validateVisibleHostPath(source, hostPath string) (string, error) {
	resolvedSource, err := filepath.EvalSymlinks(source)
	if err != nil {
		return "", err
	}
	resolvedHostPath, err := filepath.EvalSymlinks(hostPath)
	if err != nil {
		return "", err
	}
	info, err := os.Stat(resolvedHostPath)
	if err != nil {
		return "", err
	}
	if !info.IsDir() {
		return "", fmt.Errorf("repository path is not a directory: %s", hostPath)
	}
	if _, err := relativeTo(resolvedSource, resolvedHostPath); err != nil {
		return "", fmt.Errorf("repository path escapes visible project: %w", err)
	}
	return resolvedHostPath, nil
}

func (s *Sandbox) virtualHomePath(path string) (string, error) {
	rel, err := relativeTo(s.paths.Home, path)
	if err != nil {
		return "", err
	}
	if rel == "." {
		return "/", nil
	}
	return "/" + filepath.ToSlash(rel), nil
}

func (s *Sandbox) runtimeBind(path string) []string {
	return []string{"--ro-bind-try", path, path}
}

func bindFlag(kind tool.BindType, optional bool) string {
	suffix := ""
	if optional {
		suffix = "-try"
	}
	switch kind {
	case tool.BindRegular, "":
		return "--bind" + suffix
	case tool.BindReadOnly:
		return "--ro-bind" + suffix
	case tool.BindDev:
		return "--dev-bind" + suffix
	default:
		return "--bind" + suffix
	}
}

func (s *Sandbox) Run(ctx context.Context, argv []string, mounts CommandMounts, env tool.Environment, opts tool.ExecOptions) (int, error) {
	cmd := s.BuildCommand(argv, mounts)
	return s.runner.Run(ctx, cmd, env, executil.Options{HideOutput: opts.HideOutput})
}
