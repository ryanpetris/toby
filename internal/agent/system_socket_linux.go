//go:build linux

package agent

// Selects the normal agent socket before the PID-1-managed fallback.

import (
	"errors"
	"fmt"
	"os"
	"os/user"
	"path/filepath"
	"strings"
)

const systemAgentSocketRoot = "/run/toby/users"

func preferredAgentSocket(normal string) (string, bool) {
	current, err := user.Current()
	if err != nil {
		return normal, false
	}

	systemd, err := systemAgentSocketForUser(current.Username)
	if err != nil {
		return normal, false
	}

	return firstAgentSocket(normal, systemd)
}

func firstAgentSocket(primary, fallback string) (string, bool) {
	if agentSocketExists(primary) || !agentSocketExists(fallback) {
		return primary, false
	}

	return fallback, true
}

func agentSocketExists(path string) bool {
	_, err := os.Lstat(path)
	return err == nil || !errors.Is(err, os.ErrNotExist)
}

func systemAgentSocketForUser(username string) (string, error) {
	if username == "" ||
		strings.IndexByte(username, 0) >= 0 ||
		filepath.Base(username) != username {
		return "", fmt.Errorf(
			"current user name %q cannot identify a system agent socket",
			username,
		)
	}

	return filepath.Join(
		systemAgentSocketRoot,
		username,
		"toby",
		"agent.sock",
	), nil
}
