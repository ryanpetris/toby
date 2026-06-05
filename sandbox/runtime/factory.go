// Package runtime is the host-side sandbox runtime: it turns a launch's
// tools.Options into a run Spec (resolving and validating projects), constructs
// the Docker-backed Instance that drives the container phases, and implements
// the tool-facing sandbox.Service by brokering filesystem/env/command
// operations over the control channel. Docker (via the Docker Engine API, which
// also serves Podman through DOCKER_HOST) is the only backend; there is no
// runtime selection.
package runtime

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	pathpkg "path"
	"path/filepath"
	"strings"
	"time"

	"petris.dev/toby/config"
	"petris.dev/toby/container/engine"
	"petris.dev/toby/diagnostic/exitcode"
	"petris.dev/toby/tools"
)

// Spec is the resolved description of a sandbox to create: its label, the
// projects to mount, the working directory, and the image/build to run.
type Spec struct {
	Label    string
	Projects []Project
	Workdir  string
	Image    string
	Build    tools.Build
}

type Project struct {
	Name     string
	HostPath string
}

// Factory turns tools.Options into a runnable Docker Instance.
type Factory struct {
	paths      config.Paths
	containers *engine.Service
}

// NewFactory builds the sandbox Factory. It is the fx constructor for the
// runtime module.
func NewFactory(paths config.Paths, containers *engine.Service) Factory {
	return Factory{paths: paths, containers: containers}
}

func (f Factory) FromOptions(opts *tools.Options) (Instance, error) {
	if opts == nil {
		opts = &tools.Options{}
	}

	spec, err := f.specFromOptions(opts)
	if err != nil {
		return nil, err
	}

	return f.newInstance(spec)
}

// newInstance constructs the Docker instance for a resolved spec, defaulting the
// image when neither an image nor a build is configured. It touches no daemon.
func (f Factory) newInstance(spec Spec) (Instance, error) {
	image := spec.Image
	if image == "" && !spec.Build.IsSet() {
		image = DefaultImage
	}

	base, err := NewBaseInstance(BaseInstanceParams{
		Label:    spec.Label,
		Workdir:  spec.Workdir,
		Projects: spec.Projects,
	})
	if err != nil {
		return nil, err
	}

	return &instance{
		BaseInstance:  base,
		containers:    f.containers,
		image:         image,
		build:         spec.Build,
		containerName: fmt.Sprintf("toby-%d-%d", os.Getpid(), time.Now().UnixNano()),
	}, nil
}

func (f Factory) specFromOptions(opts *tools.Options) (Spec, error) {
	var spec Spec
	var err error
	if len(opts.Projects) > 0 {
		spec, err = f.configuredSpec(opts)
	} else {
		spec, err = f.persistentSpec(opts)
	}
	if err != nil {
		return Spec{}, err
	}

	spec.Image = strings.TrimSpace(opts.Image)
	spec.Build = opts.Build
	return spec, nil
}

func (f Factory) configuredSpec(opts *tools.Options) (Spec, error) {
	env := filepath.ToSlash(strings.TrimSpace(opts.Env))
	if env == "" {
		env = filepath.ToSlash(strings.TrimSpace(opts.Projects[0].Name))
	}
	if err := validateRelativeName("sandbox name", env); err != nil {
		return Spec{}, exitcode.New(2, "%s", err)
	}
	projects := make([]Project, 0, len(opts.Projects))
	seen := map[string]bool{}
	for _, configured := range opts.Projects {
		project, err := f.resolveConfiguredProject(configured)
		if err != nil {
			return Spec{}, err
		}
		if seen[project.Name] {
			return Spec{}, exitcode.New(2, "duplicate configured project name: %s", project.Name)
		}
		seen[project.Name] = true
		projects = append(projects, project)
	}
	return Spec{
		Label:    env,
		Projects: projects,
		Workdir:  opts.Workdir,
	}, nil
}

func (f Factory) persistentSpec(opts *tools.Options) (Spec, error) {
	if opts.Env == "" {
		return Spec{}, exitcode.New(2, "environment name is required")
	}
	env := filepath.ToSlash(strings.TrimSpace(opts.Env))
	if err := validateRelativeName("sandbox name", env); err != nil {
		return Spec{}, exitcode.New(2, "%s", err)
	}
	projectDir, err := f.resolveProjectDir(env, opts.Project)
	if err != nil {
		return Spec{}, err
	}
	name, err := f.projectName(projectDir)
	if err != nil {
		return Spec{}, err
	}
	return Spec{
		Label:    env,
		Projects: []Project{{Name: name, HostPath: projectDir}},
		Workdir:  opts.Workdir,
	}, nil
}

func (f Factory) resolveProjectDir(envName, project string) (string, error) {
	var raw string
	switch {
	case project == "":
		if envName == "" {
			return "", nil
		}
		raw = filepath.Join(f.paths.ProjectRoot, envName)
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

func (f Factory) resolveConfiguredProject(project tools.ProjectMount) (Project, error) {
	name := filepath.ToSlash(strings.TrimSpace(project.Name))
	if err := validateRelativeName("project name", name); err != nil {
		return Project{}, exitcode.New(2, "%s", err)
	}
	source := strings.TrimSpace(project.Source)
	if source == "" {
		return Project{}, exitcode.New(2, "configured project %s source is required", name)
	}
	source = config.ExpandHome(source, f.paths.Home)
	info, err := os.Stat(source)
	if err != nil || !info.IsDir() {
		return Project{}, exitcode.New(1, "failed to resolve project directory: %s does not exist", source)
	}
	return Project{Name: name, HostPath: source}, nil
}

func (f Factory) projectName(hostPath string) (string, error) {
	name := filepath.Base(hostPath)
	if err := validateRelativeName("project name", name); err != nil {
		return "", err
	}
	return name, nil
}

func newControlToken() (string, error) {
	var data [32]byte
	if _, err := rand.Read(data[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(data[:]), nil
}

func validateRelativeName(label, value string) error {
	value = filepath.ToSlash(strings.TrimSpace(value))
	if value == "" || pathpkg.IsAbs(value) || strings.ContainsRune(value, 0) || strings.Contains(value, "/") {
		return fmt.Errorf("invalid %s: %q", label, value)
	}
	for _, segment := range strings.Split(value, "/") {
		if segment == "" || segment == "." || segment == ".." {
			return fmt.Errorf("invalid %s: %q", label, value)
		}
	}
	return nil
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
