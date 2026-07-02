// Package config resolves Toby's host filesystem paths — the home, XDG config
// directory, projects root, and sandbox root — and provides home-directory
// expansion for config values.
package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

type Paths struct {
	Home          string
	XDGConfigHome string
	ProjectRoot   string
	SandboxRoot   string
	RuntimeDir    string
}

func NewPaths() (Paths, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return Paths{}, err
	}
	return Paths{
		Home:          home,
		XDGConfigHome: configHome(home),
		ProjectRoot:   envPath("XDG_PROJECTS_DIR", filepath.Join(home, "Projects")),
		SandboxRoot:   sandboxRoot(home),
		RuntimeDir:    runtimeDir(),
	}, nil
}

func (p Paths) TobyConfigDir() string {
	return filepath.Join(p.XDGConfigHome, "toby")
}

func configHome(home string) string {
	if value := os.Getenv("XDG_CONFIG_HOME"); value != "" {
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
	cacheHome := envPath("XDG_CACHE_HOME", filepath.Join(home, ".cache"))
	return filepath.Join(cacheHome, "toby", "sandboxes")
}

// runtimeDir resolves the per-user runtime directory used for the daemon's
// transport endpoint. It prefers XDG_RUNTIME_DIR (a tmpfs the OS scopes and cleans
// up per login session) and falls back to a uid-scoped subdirectory of the temp
// dir so the endpoint is never world-writable. The concrete daemon.sock/daemon.lock
// naming lives in the unix-socket transport, not here.
func runtimeDir() string {
	if value := os.Getenv("XDG_RUNTIME_DIR"); value != "" {
		return expandHome(value)
	}
	return filepath.Join(os.TempDir(), fmt.Sprintf("toby-%d", os.Getuid()))
}

func ExpandHome(path, home string) string {
	if path == "~" {
		return home
	}
	if strings.HasPrefix(path, "~/") {
		return home + path[1:]
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
