package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

type Paths struct {
	Home           string
	XDGConfigHome  string
	ProjectRoot    string
	SandboxRoot    string
	XDGRuntimeDir  string
	PipewireCore   string
	WaylandDisplay string
	XAuthority     string
}

func NewPaths() (Paths, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return Paths{}, err
	}
	runtimeDir := os.Getenv("XDG_RUNTIME_DIR")
	if runtimeDir == "" {
		return Paths{}, fmt.Errorf("XDG_RUNTIME_DIR is required")
	}
	return Paths{
		Home:           home,
		XDGConfigHome:  configHome(home),
		ProjectRoot:    envPath("XDG_PROJECTS_DIR", filepath.Join(home, "Projects")),
		SandboxRoot:    sandboxRoot(home),
		XDGRuntimeDir:  ExpandHome(runtimeDir, home),
		PipewireCore:   envString("PIPEWIRE_CORE", "pipewire-0"),
		WaylandDisplay: envString("WAYLAND_DISPLAY", "wayland-0"),
		XAuthority:     os.Getenv("XAUTHORITY"),
	}, nil
}

func (p Paths) TobyConfigDir() string {
	return filepath.Join(p.XDGConfigHome, "toby")
}

func configHome(home string) string {
	if value := os.Getenv("XDG_CONFIG_HOME"); value != "" {
		return ExpandHome(value, home)
	}
	if value := os.Getenv("XDG_CONFIG_DIR"); value != "" {
		return ExpandHome(value, home)
	}
	return filepath.Join(home, ".config")
}

func envString(name, fallback string) string {
	if value := os.Getenv(name); value != "" {
		return value
	}
	return fallback
}

func envPath(name, fallback string) string {
	return expandHome(envString(name, fallback))
}

func sandboxRoot(home string) string {
	if value := os.Getenv("TOBY_SANDBOX_ROOT"); value != "" {
		return ExpandHome(value, home)
	}
	cacheHome := envPath("XDG_CACHE_HOME", filepath.Join(home, ".cache"))
	return filepath.Join(cacheHome, "toby", "sandboxes")
}

func ExpandHome(path, home string) string {
	if path == "~" {
		return home
	}
	if strings.HasPrefix(path, "~/") {
		return filepath.Join(home, path[2:])
	}
	return path
}

func expandHome(path string) string {
	home, err := os.UserHomeDir()
	if err != nil {
		return path
	}
	return ExpandHome(path, home)
}
