package runtime

// Project mounts and container-interior path resolution for a session. Projects
// holds the resolved project→host-path mounts (from the launch spec) and answers
// the tool-facing path queries: project-name → sandbox path, repository visibility
// (host path behind a visible project), and the well-known sandbox directories.

import (
	"fmt"
	"os"
	pathpkg "path"
	"path/filepath"
	"strings"

	"petris.dev/toby/container/layout"
)

// DefaultImage is the image used when no image or build is configured.
const DefaultImage = "mcr.microsoft.com/devcontainers/javascript-node:24-bookworm"

// defaultCols and defaultRows seed a headless foreground PTY (no host terminal to
// measure) so the tool sees a plausible terminal size instead of zero.
const (
	defaultCols = 80
	defaultRows = 24
)

// RuntimeInfo is the runtime's self-description for session introspection.
type RuntimeInfo struct {
	Runtime string
	Info    map[string]any
}

// MountInfo is a persistent volume mount reported for session introspection.
type MountInfo struct {
	Key      string
	Profile  string
	Volume   string
	Target   string
	Access   string
	Optional bool
}

// ProjectMount is one project mounted into the sandbox workspace.
type ProjectMount struct {
	Name        string
	HostPath    string
	SandboxPath string
}

// Projects resolves tool-facing paths against a session's mounted projects.
type Projects struct {
	workdir  string
	projects []ProjectMount
}

func newProjects(workdir string, projects []Project) Projects {
	mounts := make([]ProjectMount, 0, len(projects))
	for _, project := range projects {
		mounts = append(mounts, ProjectMount{
			Name:        project.Name,
			HostPath:    project.HostPath,
			SandboxPath: filepath.Join(layout.Workspace, filepath.FromSlash(project.Name)),
		})
	}
	return Projects{workdir: workdir, projects: mounts}
}

func (p Projects) ProjectMounts() []ProjectMount {
	return append([]ProjectMount(nil), p.projects...)
}

func (p Projects) HomeDir() string  { return layout.Home }
func (p Projects) Projects() string { return layout.Workspace }

func (p Projects) ProjectPath(name string) (string, bool) {
	name = filepath.ToSlash(strings.TrimSpace(name))
	for _, project := range p.projects {
		if project.Name == name {
			return project.SandboxPath, true
		}
	}
	return "", false
}

func (p Projects) TobyRuntimeDir() string { return layout.Root }
func (p Projects) TobyBinDir() string     { return layout.Bin }
func (p Projects) TobyBinaryPath() string { return filepath.Join(layout.Bin, "toby") }

// ChdirDir is the working directory a launched tool starts in.
func (p Projects) ChdirDir() string {
	if p.workdir != "" {
		return layout.Expand(p.workdir)
	}
	if len(p.projects) > 0 {
		return p.projects[0].SandboxPath
	}
	return layout.Workspace
}

func (p Projects) VisibleHostPath(repository string) (string, error) {
	repository, err := repositoryName(repository)
	if err != nil {
		return "", err
	}
	var selected *ProjectMount
	selectedName := ""
	for i := range p.projects {
		if nameWithin(p.projects[i].Name, repository) && len(p.projects[i].Name) > len(selectedName) {
			selected = &p.projects[i]
			selectedName = p.projects[i].Name
		}
	}
	if selected == nil {
		return "", fmt.Errorf("repository is outside visible project: %s", repository)
	}
	rel := strings.TrimPrefix(repository, selected.Name)
	rel = strings.TrimPrefix(rel, "/")
	hostPath := selected.HostPath
	if rel != "" {
		hostPath = filepath.Join(hostPath, filepath.FromSlash(rel))
	}
	return validateVisibleHostPath(selected.HostPath, hostPath)
}

func repositoryName(repository string) (string, error) {
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
	return strings.Join(segments, "/"), nil
}

func nameWithin(base, name string) bool {
	return name == base || strings.HasPrefix(name, base+"/")
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

func stdinIsTerminal() bool  { return isCharDevice(os.Stdin) }
func stdoutIsTerminal() bool { return isCharDevice(os.Stdout) }

func isCharDevice(file *os.File) bool {
	info, err := file.Stat()
	return err == nil && info.Mode()&os.ModeCharDevice != 0
}
