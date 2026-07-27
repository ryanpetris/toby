package bwrap

// Adapts tool lifecycle declarations and command execution to one Bubblewrap
// Run without introducing a per-home or per-application lock.

import (
	"context"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"sync"

	"petris.dev/toby/internal/diagnostic/exitcode"
	gitcap "petris.dev/toby/internal/hostaction/methods/git"
	sandboxapi "petris.dev/toby/internal/sandbox"
	"petris.dev/toby/internal/sandbox/layout"
	"petris.dev/toby/internal/sandbox/mount"
)

// ToolSandboxOptions supplies the immutable project and OCI environment view,
// foreground terminal identity, and streams exposed to tools.
type ToolSandboxOptions struct {
	Projects                []Project
	ImageEnvironment        []string
	TerminalType            string
	ForegroundStreams       ProcessIO
	StartLifecycleOperation LifecycleOperationFactory
	ForegroundMode          ExecutionMode
	RevealHiddenOutput      bool
}

// LifecycleOperationFactory supplies operation-scoped streams and a completion
// callback for one non-foreground sandbox command.
type LifecycleOperationFactory func(
	context.Context,
	[]string,
	sandboxapi.ExecOptions,
) (ProcessIO, func(error))

// ToolSandbox collects host-phase tool declarations and, once attached,
// executes every in-sandbox lifecycle command through one shared Run.
type ToolSandbox struct {
	mu sync.Mutex

	projects                map[string]Project
	imageEnvironment        map[string]string
	environment             map[string]string
	mountRequests           map[mount.Key]mount.Request
	binds                   map[string]mount.Bind
	foregroundStreams       ProcessIO
	startLifecycleOperation LifecycleOperationFactory
	foregroundMode          ExecutionMode
	revealHiddenOutput      bool
	run                     *Run
	attached                bool
}

var _ sandboxapi.Service = (*ToolSandbox)(nil)
var _ gitcap.RepositoryResolver = (*ToolSandbox)(nil)

// NewToolSandbox creates a run-local tool-facing sandbox in declaration mode.
// Attach must be called after tool host/configuration phases have completed.
func NewToolSandbox(options ToolSandboxOptions) (*ToolSandbox, error) {
	mode := options.ForegroundMode
	if mode == "" {
		mode = ExecutionDirectTerminal
	}
	if mode != ExecutionNonInteractive &&
		mode != ExecutionDirectTerminal &&
		mode != ExecutionManagedPTY {
		return nil, fmt.Errorf("invalid foreground execution mode %q", mode)
	}

	projects := make(map[string]Project, len(options.Projects))
	for _, project := range options.Projects {
		if _, found := projects[project.Name]; found {
			return nil, fmt.Errorf("duplicate tool project %q", project.Name)
		}
		if project.Target != path.Join(layout.Workspace, project.Name) {
			return nil, fmt.Errorf(
				"tool project %q target must be beneath %s using its name",
				project.Name,
				layout.Workspace,
			)
		}
		projects[project.Name] = project
	}

	imageEnvironment, err := normalizeImageEnvironment(
		options.ImageEnvironment,
	)
	if err != nil {
		return nil, err
	}
	environment := cloneEnvironment(imageEnvironment)
	if mode != ExecutionNonInteractive && options.TerminalType != "" {
		if strings.ContainsRune(options.TerminalType, 0) {
			return nil, fmt.Errorf(
				"host TERM contains a NUL byte",
			)
		}
		environment["TERM"] = options.TerminalType
	}

	return &ToolSandbox{
		projects:                projects,
		imageEnvironment:        imageEnvironment,
		environment:             environment,
		mountRequests:           make(map[mount.Key]mount.Request),
		binds:                   make(map[string]mount.Bind),
		foregroundStreams:       options.ForegroundStreams,
		startLifecycleOperation: options.StartLifecycleOperation,
		foregroundMode:          mode,
		revealHiddenOutput:      options.RevealHiddenOutput,
	}, nil
}

// ValidateImageEnvironment confirms that tool configuration and the prepared
// OCI rootfs use the same base environment.
func (s *ToolSandbox) ValidateImageEnvironment(values []string) error {
	if s == nil {
		return fmt.Errorf("tool sandbox is nil")
	}

	normalized, err := normalizeImageEnvironment(values)
	if err != nil {
		return err
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	if !reflect.DeepEqual(normalized, s.imageEnvironment) {
		return fmt.Errorf(
			"tool sandbox OCI image environment does not match prepared rootfs",
		)
	}

	return nil
}

// Attach freezes declarations and binds command execution to run. The run plan
// must contain exactly the declarations collected by this sandbox.
func (s *ToolSandbox) Attach(run *Run) error {
	if run == nil {
		return fmt.Errorf("attach tool sandbox: Bubblewrap run is nil")
	}

	plan := run.Plan()
	if plan.RunID == "" {
		return fmt.Errorf("attach tool sandbox: Bubblewrap run is closed")
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	if s.attached {
		return fmt.Errorf("tool sandbox is already attached")
	}
	if err := s.validatePlanLocked(plan); err != nil {
		return fmt.Errorf("attach tool sandbox: %w", err)
	}

	s.run = run
	s.attached = true

	return nil
}

// MountRequests returns a deterministic detached copy of declarations to
// resolve through native managed-directory storage.
func (s *ToolSandbox) MountRequests() []mount.Request {
	if s == nil {
		return nil
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	result := make([]mount.Request, 0, len(s.mountRequests))
	for _, request := range s.mountRequests {
		result = append(result, request)
	}
	sort.Slice(result, func(i, j int) bool {
		if result[i].Target == result[j].Target {
			return result[i].Key.String() < result[j].Key.String()
		}
		return result[i].Target < result[j].Target
	})

	return result
}

// Binds returns a deterministic detached copy of external-bind declarations.
func (s *ToolSandbox) Binds() []mount.Bind {
	if s == nil {
		return nil
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	result := make([]mount.Bind, 0, len(s.binds))
	for _, bind := range s.binds {
		result = append(result, bind)
	}
	sort.Slice(result, func(i, j int) bool {
		if result[i].Target == result[j].Target {
			return result[i].HostPath < result[j].HostPath
		}
		return result[i].Target < result[j].Target
	})

	return result
}

// EnvironmentVariables returns the deterministic environment fixed into the
// run plan after tool configuration.
func (s *ToolSandbox) EnvironmentVariables() []EnvironmentVariable {
	if s == nil {
		return nil
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	result := make([]EnvironmentVariable, 0, len(s.environment))
	for name, value := range s.environment {
		result = append(result, EnvironmentVariable{Name: name, Value: value})
	}
	sort.Slice(result, func(i, j int) bool {
		return result[i].Name < result[j].Name
	})

	return result
}

// ProjectPath returns the configured project path.
func (s *ToolSandbox) ProjectPath(name string) (string, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()

	project, found := s.projects[name]
	return project.Target, found
}

// VisibleHostPath resolves a host path visible to the sandbox.
func (s *ToolSandbox) VisibleHostPath(repository string) (string, error) {
	if repository == "" || strings.ContainsRune(repository, 0) {
		return "", fmt.Errorf("invalid repository path %q", repository)
	}

	cleaned := path.Clean(repository)
	if cleaned != repository || cleaned == "." || strings.HasPrefix(cleaned, "../") {
		return "", fmt.Errorf("invalid repository path %q", repository)
	}
	name, suffix, _ := strings.Cut(cleaned, "/")

	s.mu.Lock()
	project, found := s.projects[name]
	s.mu.Unlock()
	if !found {
		return "", fmt.Errorf("repository %q is not in a selected project", repository)
	}

	hostPath := project.HostPath
	if suffix != "" {
		hostPath = filepath.Join(hostPath, filepath.FromSlash(suffix))
	}
	resolvedProject, err := filepath.EvalSymlinks(project.HostPath)
	if err != nil {
		return "", fmt.Errorf("resolve project %q: %w", name, err)
	}
	resolvedHostPath, err := filepath.EvalSymlinks(hostPath)
	if err != nil {
		return "", fmt.Errorf("resolve repository %q: %w", repository, err)
	}
	info, err := os.Stat(resolvedHostPath)
	if err != nil {
		return "", fmt.Errorf("inspect repository %q: %w", repository, err)
	}
	if !info.IsDir() {
		return "", fmt.Errorf("repository %q is not a directory", repository)
	}
	relative, err := filepath.Rel(resolvedProject, resolvedHostPath)
	if err != nil || relative == ".." ||
		strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("repository %q escapes project %q", repository, name)
	}

	return resolvedHostPath, nil
}

// OpenVisibleHostDirectory opens the exact selected-project directory
// capability for repository. It never reopens a diagnostic host path.
func (s *ToolSandbox) OpenVisibleHostDirectory(
	repository string,
) (*os.File, error) {
	if repository == "" || strings.ContainsRune(repository, 0) {
		return nil, fmt.Errorf("invalid repository path %q", repository)
	}

	cleaned := path.Clean(repository)
	if cleaned != repository || cleaned == "." ||
		strings.HasPrefix(cleaned, "../") {
		return nil, fmt.Errorf("invalid repository path %q", repository)
	}
	name, suffix, _ := strings.Cut(cleaned, "/")

	s.mu.Lock()
	project, found := s.projects[name]
	run := s.run
	attached := s.attached
	s.mu.Unlock()
	if !found {
		return nil, fmt.Errorf(
			"repository %q is not in a selected project",
			repository,
		)
	}
	if !attached || run == nil {
		return nil, fmt.Errorf("tool sandbox is not attached to a run")
	}

	return run.openProjectDirectory(
		project.Name,
		filepath.FromSlash(suffix),
	)
}

// Environment returns a copy of the configured environment.
func (s *ToolSandbox) Environment(name string) (string, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()

	value, found := s.environment[name]
	return value, found
}

// SetEnvironment sets an environment variable.
func (s *ToolSandbox) SetEnvironment(_ context.Context, name, value string) error {
	if name == "" || strings.ContainsAny(name, "=\x00") ||
		name == "HOME" || name == "TOBY_SANDBOX" ||
		strings.ContainsRune(value, 0) {
		return fmt.Errorf("invalid environment variable %q", name)
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	if s.attached {
		return fmt.Errorf("tool environment is frozen after run attachment")
	}
	if value == "" {
		delete(s.environment, name)
	} else {
		s.environment[name] = value
	}

	return nil
}

// PrependEnvironment prepends a value to an environment variable.
func (s *ToolSandbox) PrependEnvironment(
	ctx context.Context,
	name string,
	value string,
	separator string,
) error {
	return s.setEnvironmentEntry(ctx, name, value, separator, true)
}

// AppendEnvironment appends a value to an environment variable.
func (s *ToolSandbox) AppendEnvironment(
	ctx context.Context,
	name string,
	value string,
	separator string,
) error {
	return s.setEnvironmentEntry(ctx, name, value, separator, false)
}

// AddBind adds a host bind to the mutable plan.
func (s *ToolSandbox) AddBind(bind mount.Bind) error {
	if bind.Access == "" {
		bind.Access = mount.AccessRegular
	}
	bind.Target = layout.ExpandHome(bind.Target)
	if err := bind.Validate(); err != nil {
		return err
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	if s.attached {
		return fmt.Errorf("tool mounts are frozen after run attachment")
	}
	if existing, found := s.binds[bind.Target]; found {
		if !reflect.DeepEqual(existing, bind) {
			return fmt.Errorf("conflicting bind target %q", bind.Target)
		}
		return nil
	}
	for _, request := range s.mountRequests {
		if mount.TargetsOverlap(request.Target, bind.Target) {
			return fmt.Errorf(
				"bind target %q overlaps managed-directory target %q",
				bind.Target,
				request.Target,
			)
		}
	}
	s.binds[bind.Target] = bind

	return nil
}

// AddMount adds a managed volume to the mutable plan.
func (s *ToolSandbox) AddMount(request mount.Request) error {
	if request.Access == "" {
		request.Access = mount.AccessRegular
	}
	request.Target = layout.ExpandHome(request.Target)
	if err := validateToolMountRequest(request); err != nil {
		return err
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	if s.attached {
		return fmt.Errorf("tool mounts are frozen after run attachment")
	}
	if existing, found := s.mountRequests[request.Key]; found {
		if !reflect.DeepEqual(existing, request) {
			return fmt.Errorf(
				"conflicting managed-directory key %s",
				request.Key,
			)
		}
		return nil
	}
	for _, existing := range s.mountRequests {
		if mount.TargetsOverlap(existing.Target, request.Target) {
			return fmt.Errorf(
				"managed-directory target %q overlaps %q",
				request.Target,
				existing.Target,
			)
		}
	}
	for _, bind := range s.binds {
		if mount.TargetsOverlap(bind.Target, request.Target) {
			return fmt.Errorf(
				"managed-directory target %q overlaps bind target %q",
				request.Target,
				bind.Target,
			)
		}
	}

	s.mountRequests[request.Key] = request

	return nil
}

// Exec executes a command in the attached sandbox.
func (s *ToolSandbox) Exec(
	ctx context.Context,
	argv []string,
	options sandboxapi.ExecOptions,
) (int, error) {
	s.mu.Lock()
	run := s.run
	attached := s.attached
	startLifecycleOperation := s.startLifecycleOperation
	streams := ProcessIO{}
	mode := ExecutionNonInteractive
	if options.Foreground {
		mode = s.foregroundMode
		streams = s.foregroundStreams
	}
	s.mu.Unlock()

	if !attached || run == nil {
		return 1, fmt.Errorf("tool sandbox is not attached to a Bubblewrap run")
	}
	finish := func(error) {}
	if !options.Foreground && startLifecycleOperation != nil {
		streams, finish = startLifecycleOperation(ctx, argv, options)
		if finish == nil {
			finish = func(error) {}
		}
	}
	if options.HideOutput && !s.revealHiddenOutput {
		streams.Stdout = io.Discard
		streams.Stderr = io.Discard
	}

	capabilities := CapabilityDropAll
	if options.Root {
		capabilities = CapabilityRootLifecycle
	}
	command := Command{
		Argv:         append([]string(nil), argv...),
		Mode:         mode,
		Root:         options.Root,
		Capabilities: capabilities,
	}

	code, err := run.Execute(ctx, command, streams)
	if err != nil {
		finish(err)
		return code, err
	}
	if code != 0 {
		err = exitcode.Code(code)
		finish(err)
		return code, err
	}

	finish(nil)
	return code, nil
}

func (s *ToolSandbox) setEnvironmentEntry(
	ctx context.Context,
	name string,
	value string,
	separator string,
	atStart bool,
) error {
	if separator == "" {
		separator = ":"
	}
	if strings.ContainsRune(separator, 0) || strings.ContainsRune(value, 0) {
		return fmt.Errorf("invalid environment path entry")
	}

	current, _ := s.Environment(name)
	parts := strings.Split(current, separator)
	result := make([]string, 0, len(parts)+1)
	if atStart {
		result = append(result, value)
	}
	for _, part := range parts {
		if part == "" || part == value {
			continue
		}
		result = append(result, part)
	}
	if !atStart {
		result = append(result, value)
	}

	return s.SetEnvironment(ctx, name, strings.Join(result, separator))
}

func (s *ToolSandbox) validatePlanLocked(plan Plan) error {
	requests := make([]mount.Request, 0, len(s.mountRequests))
	for _, request := range s.mountRequests {
		requests = append(requests, request)
	}
	sort.Slice(requests, func(i, j int) bool {
		return requests[i].Key.String() < requests[j].Key.String()
	})

	entries := append([]mount.Entry(nil), plan.ManagedDirectories...)
	sort.Slice(entries, func(i, j int) bool {
		return entries[i].Key.String() < entries[j].Key.String()
	})
	if len(requests) != len(entries) {
		return fmt.Errorf(
			"run has %d managed directories, tool declarations require %d",
			len(entries),
			len(requests),
		)
	}
	for index, request := range requests {
		entry := entries[index]
		if request.Key != entry.Key ||
			request.Target != entry.Target ||
			request.Access != entry.Access ||
			request.Optional != entry.Optional ||
			request.Seed != entry.Seed {
			return fmt.Errorf(
				"run managed directory %s does not match tool declaration",
				entry.Key,
			)
		}
	}

	if err := s.validateBindsLocked(plan.Binds); err != nil {
		return err
	}
	wantEnvironment := s.environmentLocked()
	if !reflect.DeepEqual(wantEnvironment, plan.Environment) {
		return fmt.Errorf("run environment does not match tool configuration")
	}

	projects := make([]Project, 0, len(s.projects))
	for _, project := range s.projects {
		projects = append(projects, project)
	}
	sort.Slice(projects, func(i, j int) bool {
		return projects[i].Name < projects[j].Name
	})
	if len(projects) == 0 {
		projects = nil
	}
	if !reflect.DeepEqual(projects, plan.Projects) {
		return fmt.Errorf("run projects do not match tool project view")
	}

	return nil
}

func (s *ToolSandbox) validateBindsLocked(selected []mount.Bind) error {
	seen := make(map[string]struct{}, len(selected))
	for _, bind := range selected {
		declared, found := s.binds[bind.Target]
		if !found || !reflect.DeepEqual(declared, bind) {
			return fmt.Errorf(
				"run external bind %q does not match a tool declaration",
				bind.Target,
			)
		}
		if _, found := seen[bind.Target]; found {
			return fmt.Errorf("run external bind %q is duplicated", bind.Target)
		}
		seen[bind.Target] = struct{}{}
	}
	for target, bind := range s.binds {
		if bind.Optional {
			continue
		}
		if _, found := seen[target]; !found {
			return fmt.Errorf(
				"required tool bind %q is absent from the run",
				target,
			)
		}
	}

	return nil
}

func (s *ToolSandbox) environmentLocked() []EnvironmentVariable {
	if len(s.environment) == 0 {
		return nil
	}

	result := make([]EnvironmentVariable, 0, len(s.environment))
	for name, value := range s.environment {
		result = append(result, EnvironmentVariable{Name: name, Value: value})
	}
	sort.Slice(result, func(i, j int) bool {
		return result[i].Name < result[j].Name
	})
	return result
}

func normalizeImageEnvironment(
	values []string,
) (map[string]string, error) {
	result := make(map[string]string, len(values))
	for _, entry := range values {
		name, value, found := strings.Cut(entry, "=")
		if !found || name == "" || strings.ContainsAny(name, "=\x00") {
			return nil, fmt.Errorf(
				"invalid OCI image environment entry %q",
				entry,
			)
		}
		if strings.ContainsRune(value, 0) {
			return nil, fmt.Errorf(
				"OCI image environment variable %q contains a NUL byte",
				name,
			)
		}
		if name == "HOME" || name == "TOBY_SANDBOX" {
			continue
		}

		result[name] = value
	}

	return result, nil
}

func cloneEnvironment(values map[string]string) map[string]string {
	result := make(map[string]string, len(values))
	for name, value := range values {
		result[name] = value
	}

	return result
}

func validateToolMountRequest(request mount.Request) error {
	if err := request.Key.Validate(); err != nil {
		return err
	}
	if err := mount.ValidateTarget(request.Target); err != nil {
		return err
	}
	if err := request.Access.Validate(); err != nil {
		return err
	}
	return request.Seed.Validate()
}
