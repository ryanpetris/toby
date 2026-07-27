// Package config resolves Toby's host filesystem paths and provides
// home-directory expansion for config values.
package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Paths contains resolved host and XDG directories used by Toby.
type Paths struct {
	Home          string
	XDGConfigHome string
	XDGCacheHome  string
	XDGDataHome   string
	XDGRuntimeDir string
	ProjectRoot   string
}

// NewPaths resolves Toby's host paths from the current environment.
func NewPaths() (Paths, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return Paths{}, err
	}
	configHome, err := xdgPath("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	if err != nil {
		return Paths{}, err
	}
	cacheHome, err := xdgPath("XDG_CACHE_HOME", filepath.Join(home, ".cache"))
	if err != nil {
		return Paths{}, err
	}
	dataHome, err := xdgPath("XDG_DATA_HOME", filepath.Join(home, ".local", "share"))
	if err != nil {
		return Paths{}, err
	}
	projectRoot, err := xdgPath("XDG_PROJECTS_DIR", filepath.Join(home, "Projects"))
	if err != nil {
		return Paths{}, err
	}
	runtimeDir, err := optionalAbsolutePath("XDG_RUNTIME_DIR")
	if err != nil {
		return Paths{}, err
	}

	return Paths{
		Home:          home,
		XDGConfigHome: configHome,
		XDGCacheHome:  cacheHome,
		XDGDataHome:   dataHome,
		XDGRuntimeDir: runtimeDir,
		ProjectRoot:   projectRoot,
	}, nil
}

// TobyConfigDir returns Toby's per-user configuration directory.
func (p Paths) TobyConfigDir() string {
	return filepath.Join(p.XDGConfigHome, "toby")
}

// TobyDataDir returns Toby's per-user data directory.
func (p Paths) TobyDataDir() string {
	return filepath.Join(p.XDGDataHome, "toby")
}

// TobyCacheDir returns Toby's per-user cache directory.
func (p Paths) TobyCacheDir() string {
	return filepath.Join(p.XDGCacheHome, "toby")
}

// RunStorageDir returns the directory for transient sandbox run data.
func (p Paths) RunStorageDir() string {
	return filepath.Join(p.TobyCacheDir(), "runs")
}

// RuntimePaths are the per-user paths used by the agent and runs.
// ResolveRuntime validates XDG_RUNTIME_DIR before deriving them, so commands
// that only show help or inspect static configuration do not require a runtime
// directory.
type RuntimePaths struct {
	Root        string
	Runs        string
	Caddy       string
	AgentSocket string
}

// ResolveRuntime resolves the per-user runtime directory and agent socket.
func (p Paths) ResolveRuntime() (RuntimePaths, error) {
	if err := validateRuntimeDir(p.XDGRuntimeDir); err != nil {
		return RuntimePaths{}, err
	}
	root := filepath.Join(p.XDGRuntimeDir, "toby")
	return RuntimePaths{
		Root:        root,
		Runs:        filepath.Join(root, "runs"),
		Caddy:       filepath.Join(root, "caddy"),
		AgentSocket: filepath.Join(root, "agent.sock"),
	}, nil
}

func xdgPath(name, fallback string) (string, error) {
	value := strings.TrimSpace(os.Getenv(name))
	if value == "" {
		value = fallback
	}
	if !filepath.IsAbs(value) {
		return "", fmt.Errorf("%s must be an absolute path: %q", name, value)
	}
	return filepath.Clean(value), nil
}

func optionalAbsolutePath(name string) (string, error) {
	value := strings.TrimSpace(os.Getenv(name))
	if value == "" {
		return "", nil
	}
	if !filepath.IsAbs(value) {
		return "", fmt.Errorf("%s must be an absolute path: %q", name, value)
	}
	return filepath.Clean(value), nil
}

// ExpandHome expands a leading tilde relative to home.
func ExpandHome(path, home string) string {
	if path == "~" {
		return home
	}
	if strings.HasPrefix(path, "~/") {
		return home + path[1:]
	}
	return path
}
