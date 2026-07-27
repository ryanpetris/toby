// Package bwrap defines and executes deterministic Bubblewrap sandbox plans.
package bwrap

import (
	"fmt"
	"io/fs"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"petris.dev/toby/internal/sandbox/layout"
	"petris.dev/toby/internal/sandbox/mount"
)

var (
	digestPattern = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)
	idPattern     = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*$`)
)

// ExecutionMode selects foreground terminal behavior.
type ExecutionMode string

const (
	// ExecutionNonInteractive uses ordinary non-terminal streams.
	ExecutionNonInteractive ExecutionMode = "noninteractive"
	// ExecutionDirectTerminal attaches directly to the host terminal.
	ExecutionDirectTerminal ExecutionMode = "direct_terminal"
	// ExecutionManagedPTY uses a Toby-managed pseudoterminal.
	ExecutionManagedPTY ExecutionMode = "managed_pty"
)

// NetworkMode selects the Bubblewrap network namespace policy.
type NetworkMode string

const (
	// NetworkHost keeps the invoking host network namespace.
	NetworkHost NetworkMode = "host"

	// NetworkPrivate gives a process a new network namespace with only its
	// loopback interface.
	NetworkPrivate NetworkMode = "private"
)

// CapabilityPolicy selects one closed set of process capabilities after
// Bubblewrap has constructed the sandbox.
type CapabilityPolicy string

const (
	// CapabilityDropAll gives an application command no capabilities.
	CapabilityDropAll CapabilityPolicy = "drop_all"

	// CapabilityRootLifecycle gives namespace-root lifecycle commands only the
	// ownership, DAC, and identity capabilities required by tool installers.
	CapabilityRootLifecycle CapabilityPolicy = "root_lifecycle"
)

// Plan is the complete pure description of one Bubblewrap command. Sequential
// lifecycle commands for one run share its RootFS, Overlay, Home, and mounts but
// receive their own Command.
type Plan struct {
	RunID              string
	RootFS             RootFS
	Overlay            Overlay
	Home               Home
	Projects           []Project
	ManagedDirectories []mount.Entry
	Binds              []mount.Bind
	RuntimeAssets      []RuntimeAsset
	GeneratedFiles     []GeneratedFile
	SandboxBinary      Binary
	Workdir            string
	Environment        []EnvironmentVariable
	Identity           Identity
	Namespaces         Namespaces
	Command            Command
}

// RootFS identifies the immutable image root filesystem.
type RootFS struct {
	Digest string
	Path   string
}

// Overlay contains writable root-overlay directories.
type Overlay struct {
	RunStorageDir string
	Upper         string
	Work          string
}

// Home identifies the persistent private-home mount.
type Home struct {
	ID       string
	HostPath string
}

// Project describes one project mount.
type Project struct {
	Name     string
	HostPath string
	Target   string
	ReadOnly bool
}

// GeneratedFile describes one host-generated sandbox file.
type GeneratedFile struct {
	HostPath string
	Target   string
	Data     []byte
	Mode     fs.FileMode
	UID      int
	GID      int
}

// RuntimeAsset exposes one transient, explicitly opened source beneath
// /run/toby. Runtime assets are separate from generic external binds so the
// reserved runtime namespace stays protected.
type RuntimeAsset struct {
	HostPath string
	Target   string
	Access   mount.Access
}

// Binary describes one executable mounted into the sandbox.
type Binary struct {
	HostPath string
	Target   string
}

// EnvironmentVariable is one exact process environment entry.
type EnvironmentVariable struct {
	Name  string
	Value string
}

// Identity contains the host user and group mapped into the sandbox.
type Identity struct {
	HostUID int
	HostGID int
}

// Namespaces selects optional namespace isolation.
type Namespaces struct {
	Network NetworkMode
}

// Command describes the sandbox payload and its execution mode.
type Command struct {
	Argv         []string
	Mode         ExecutionMode
	Root         bool
	Capabilities CapabilityPolicy
}

// Canonical returns a deterministically ordered copy of the plan.
func (p Plan) Canonical() Plan {
	clone := p.Clone()
	sort.Slice(clone.Projects, func(i, j int) bool {
		return clone.Projects[i].Name < clone.Projects[j].Name
	})
	sort.Slice(clone.ManagedDirectories, func(i, j int) bool {
		if clone.ManagedDirectories[i].Target == clone.ManagedDirectories[j].Target {
			return clone.ManagedDirectories[i].Key.String() < clone.ManagedDirectories[j].Key.String()
		}
		return clone.ManagedDirectories[i].Target < clone.ManagedDirectories[j].Target
	})
	sort.Slice(clone.Binds, func(i, j int) bool {
		if clone.Binds[i].Target == clone.Binds[j].Target {
			return clone.Binds[i].HostPath < clone.Binds[j].HostPath
		}
		return clone.Binds[i].Target < clone.Binds[j].Target
	})
	sort.Slice(clone.RuntimeAssets, func(i, j int) bool {
		if clone.RuntimeAssets[i].Target == clone.RuntimeAssets[j].Target {
			return clone.RuntimeAssets[i].HostPath < clone.RuntimeAssets[j].HostPath
		}
		return clone.RuntimeAssets[i].Target < clone.RuntimeAssets[j].Target
	})
	sort.Slice(clone.GeneratedFiles, func(i, j int) bool {
		return clone.GeneratedFiles[i].HostPath < clone.GeneratedFiles[j].HostPath
	})
	sort.Slice(clone.Environment, func(i, j int) bool {
		return clone.Environment[i].Name < clone.Environment[j].Name
	})
	return clone
}

// Clone returns an independent copy of the plan.
func (p Plan) Clone() Plan {
	clone := p
	clone.Projects = append([]Project(nil), p.Projects...)
	clone.ManagedDirectories = append([]mount.Entry(nil), p.ManagedDirectories...)
	clone.Binds = append([]mount.Bind(nil), p.Binds...)
	clone.RuntimeAssets = append([]RuntimeAsset(nil), p.RuntimeAssets...)
	clone.GeneratedFiles = make([]GeneratedFile, len(p.GeneratedFiles))
	for i, file := range p.GeneratedFiles {
		clone.GeneratedFiles[i] = file
		clone.GeneratedFiles[i].Data = append([]byte(nil), file.Data...)
	}
	clone.Environment = append([]EnvironmentVariable(nil), p.Environment...)
	clone.Command.Argv = append([]string(nil), p.Command.Argv...)
	return clone
}

// Validate verifies the complete Bubblewrap plan.
func (p Plan) Validate() error {
	if !idPattern.MatchString(p.RunID) {
		return fmt.Errorf("invalid run id %q", p.RunID)
	}
	if !digestPattern.MatchString(p.RootFS.Digest) {
		return fmt.Errorf("invalid rootfs digest %q", p.RootFS.Digest)
	}
	for _, hostPath := range []struct {
		label string
		path  string
	}{
		{"rootfs", p.RootFS.Path},
		{"run storage", p.Overlay.RunStorageDir},
		{"overlay upper", p.Overlay.Upper},
		{"overlay work", p.Overlay.Work},
		{"private home", p.Home.HostPath},
		{"sandbox helper", p.SandboxBinary.HostPath},
	} {
		if err := validateHostPath(hostPath.label, hostPath.path); err != nil {
			return err
		}
	}
	if filepath.Clean(p.Overlay.Upper) == filepath.Clean(p.Overlay.Work) {
		return fmt.Errorf("overlay upper and work directories must differ")
	}
	if !idPattern.MatchString(p.Home.ID) {
		return fmt.Errorf("invalid private-home id %q", p.Home.ID)
	}
	if p.SandboxBinary.Target != layout.SandboxBinary() {
		return fmt.Errorf("sandbox helper target must be %s", layout.SandboxBinary())
	}
	if err := validateSandboxPath("workdir", p.Workdir); err != nil {
		return err
	}
	if p.Identity.HostUID < 0 || p.Identity.HostGID < 0 {
		return fmt.Errorf("host uid and gid must be non-negative")
	}
	if err := p.Namespaces.validate(); err != nil {
		return err
	}
	if err := p.Command.validate(); err != nil {
		return err
	}
	if err := validateProjects(p.Projects); err != nil {
		return err
	}
	if err := validateMounts(p.ManagedDirectories, p.Binds); err != nil {
		return err
	}
	if err := validateRuntimeAssets(p.RuntimeAssets); err != nil {
		return err
	}
	if err := validateTargetGraph(p); err != nil {
		return err
	}
	if err := validateGeneratedFiles(p); err != nil {
		return err
	}
	if err := validateHostStorageGraph(p); err != nil {
		return err
	}
	return validateEnvironment(p.Environment)
}

func (n Namespaces) validate() error {
	if n.Network != NetworkHost {
		return fmt.Errorf(
			"invalid network mode %q: initial Bubblewrap launches require host networking",
			n.Network,
		)
	}
	return nil
}

func (c Command) validate() error {
	if len(c.Argv) == 0 || c.Argv[0] == "" {
		return fmt.Errorf("command argv must not be empty")
	}
	for _, arg := range c.Argv {
		if strings.ContainsRune(arg, 0) {
			return fmt.Errorf("command argv contains a NUL byte")
		}
	}
	switch c.Mode {
	case ExecutionNonInteractive, ExecutionDirectTerminal, ExecutionManagedPTY:
	default:
		return fmt.Errorf("invalid execution mode %q", c.Mode)
	}
	switch {
	case c.Root && c.Capabilities != CapabilityRootLifecycle:
		return fmt.Errorf(
			"root commands require capability policy %q",
			CapabilityRootLifecycle,
		)
	case !c.Root && c.Capabilities != CapabilityDropAll:
		return fmt.Errorf(
			"application commands require capability policy %q",
			CapabilityDropAll,
		)
	default:
		return nil
	}
}

func validateProjects(projects []Project) error {
	seenNames := map[string]struct{}{}
	seenTargets := map[string]struct{}{}
	for _, project := range projects {
		if project.Name == "" ||
			project.Name == "." ||
			project.Name == ".." ||
			strings.ContainsRune(project.Name, '/') ||
			path.Base(project.Name) != project.Name {
			return fmt.Errorf("invalid project name %q", project.Name)
		}
		if err := validateHostPath("project "+project.Name, project.HostPath); err != nil {
			return err
		}
		if err := validateSandboxPath("project target", project.Target); err != nil {
			return err
		}
		if project.Target != path.Join(layout.Workspace, project.Name) {
			return fmt.Errorf("project %q target must be beneath %s using its name", project.Name, layout.Workspace)
		}
		if _, ok := seenNames[project.Name]; ok {
			return fmt.Errorf("duplicate project name %q", project.Name)
		}
		if _, ok := seenTargets[project.Target]; ok {
			return fmt.Errorf("duplicate project target %q", project.Target)
		}
		seenNames[project.Name] = struct{}{}
		seenTargets[project.Target] = struct{}{}
	}
	return nil
}

func validateMounts(managed []mount.Entry, binds []mount.Bind) error {
	targets := make([]string, 0, len(managed)+len(binds))
	managedKeys := make(map[mount.Key]struct{}, len(managed))
	for _, entry := range managed {
		if err := entry.Validate(); err != nil {
			return err
		}
		if err := validateHostPath("managed-directory", entry.HostPath); err != nil {
			return err
		}
		if _, found := managedKeys[entry.Key]; found {
			return fmt.Errorf(
				"duplicate managed-directory key %q",
				entry.Key.String(),
			)
		}
		managedKeys[entry.Key] = struct{}{}
		for _, target := range targets {
			if mount.TargetsOverlap(target, entry.Target) {
				return fmt.Errorf("overlapping sandbox mount targets %q and %q", target, entry.Target)
			}
		}
		targets = append(targets, entry.Target)
	}
	for _, bind := range binds {
		if err := bind.Validate(); err != nil {
			return err
		}
		if err := validateHostPath("bind", bind.HostPath); err != nil {
			return err
		}
		for _, target := range targets {
			if mount.TargetsOverlap(target, bind.Target) {
				return fmt.Errorf("overlapping sandbox mount targets %q and %q", target, bind.Target)
			}
		}
		targets = append(targets, bind.Target)
	}
	return nil
}

func validateRuntimeAssets(assets []RuntimeAsset) error {
	for index, asset := range assets {
		if err := validateHostPath("runtime asset", asset.HostPath); err != nil {
			return fmt.Errorf("runtime asset %d: %w", index, err)
		}
		if err := validateSandboxPath("runtime asset target", asset.Target); err != nil {
			return fmt.Errorf("runtime asset %d: %w", index, err)
		}
		if !sandboxPathContains(layout.Runtime, asset.Target) {
			return fmt.Errorf(
				"runtime asset target %q must be strictly beneath %s",
				asset.Target,
				layout.Runtime,
			)
		}
		if err := asset.Access.Validate(); err != nil {
			return fmt.Errorf("runtime asset %d: %w", index, err)
		}
		for earlier := range index {
			if mount.TargetsOverlap(assets[earlier].Target, asset.Target) {
				return fmt.Errorf(
					"overlapping runtime asset targets %q and %q",
					assets[earlier].Target,
					asset.Target,
				)
			}
		}
	}

	return nil
}

func validateEnvironment(environment []EnvironmentVariable) error {
	seen := map[string]struct{}{}
	for _, variable := range environment {
		if variable.Name == "" || strings.ContainsAny(variable.Name, "=\x00") {
			return fmt.Errorf("invalid environment variable name %q", variable.Name)
		}
		if strings.ContainsRune(variable.Value, 0) {
			return fmt.Errorf("environment variable %q contains a NUL byte", variable.Name)
		}
		if variable.Name == "HOME" || variable.Name == "TOBY_SANDBOX" {
			return fmt.Errorf(
				"environment variable %q is fixed by the sandbox runtime",
				variable.Name,
			)
		}
		if _, ok := seen[variable.Name]; ok {
			return fmt.Errorf("duplicate environment variable %q", variable.Name)
		}
		seen[variable.Name] = struct{}{}
	}
	return nil
}

func validateSandboxPath(label, value string) error {
	if value == "" || !path.IsAbs(value) || path.Clean(value) != value || strings.ContainsRune(value, 0) {
		return fmt.Errorf("%s must be a clean absolute POSIX path: %q", label, value)
	}
	return nil
}
