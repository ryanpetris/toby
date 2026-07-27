// Package executable resolves the three installed Toby command binaries.
package executable

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
)

const (
	// Client is the host launch and management CLI.
	Client = "toby"
	// Agent is the per-user agent.
	Agent = "tobyd"
	// Sandbox is the helper mounted inside sandbox instances.
	Sandbox = "tobys"
)

// Resolve finds an installed Toby companion, preferring the directory that
// contains the running executable before consulting PATH.
func Resolve(name string) (string, error) {
	if name != Client && name != Agent && name != Sandbox {
		return "", fmt.Errorf("unsupported Toby executable %q", name)
	}

	current, err := os.Executable()
	if err != nil {
		return "", fmt.Errorf("resolve running Toby executable: %w", err)
	}
	current, err = filepath.Abs(current)
	if err != nil {
		return "", fmt.Errorf("resolve absolute Toby executable: %w", err)
	}

	candidate := filepath.Join(filepath.Dir(current), name)
	if _, err := os.Stat(candidate); err == nil || !errors.Is(err, os.ErrNotExist) {
		return candidate, nil
	}

	resolved, err := exec.LookPath(name)
	if err != nil {
		return "", fmt.Errorf(
			"find required Toby executable %q beside %s or in PATH: %w",
			name,
			current,
			err,
		)
	}
	resolved, err = filepath.Abs(resolved)
	if err != nil {
		return "", fmt.Errorf(
			"resolve absolute Toby executable %q: %w",
			name,
			err,
		)
	}

	return filepath.Clean(resolved), nil
}
