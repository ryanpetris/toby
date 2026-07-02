// Package runtime is the host-side sandbox runtime for the profile-home topology.
// It resolves a launch's tools.Options into a Spec (validated projects, image), and
// implements the tool-facing sandbox.Service by brokering filesystem/env/command
// operations through the shared home manager and the per-project netns proxy. The
// daemon stands up the home, netns, and tool containers from these primitives.
// Docker (via the Docker Engine API, which also serves Podman through DOCKER_HOST)
// is the only backend; there is no runtime selection.
package runtime

import (
	"fmt"
	"os"
	pathpkg "path"
	"path/filepath"
	"strings"

	"petris.dev/toby/config"
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
	Ports    []string
}

type Project struct {
	Name     string
	HostPath string
}

// ResolvedImage returns the spec's image, defaulting to DefaultImage when neither
// an image nor a build is configured.
func (s Spec) ResolvedImage() string {
	if s.Image == "" && !s.Build.IsSet() {
		return DefaultImage
	}
	return s.Image
}

// Factory resolves tools.Options into a runnable Spec.
type Factory struct {
	paths config.Paths
}

// NewFactory builds the sandbox Factory. It is the fx constructor for the
// runtime module.
func NewFactory(paths config.Paths) Factory {
	return Factory{paths: paths}
}

// Resolve turns the launch-only options into a Spec, with the image, build, and
// published ports supplied separately from the effective config.
func (f Factory) Resolve(opts *tools.Options, image string, build tools.Build, ports []string) (Spec, error) {
	if opts == nil {
		opts = &tools.Options{}
	}

	spec, err := f.specFromOptions(opts)
	if err != nil {
		return Spec{}, err
	}
	spec.Image = strings.TrimSpace(image)
	spec.Build = build
	spec.Ports = ports

	if _, err := NewPortSpec(spec.Ports); err != nil {
		return Spec{}, exitcode.New(2, "%s", err)
	}
	return spec, nil
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
