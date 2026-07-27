package bwrap

// Resolves the Bubblewrap program and builds the fixed namespace argument and
// inherited-descriptor conventions used by rendered invocations.

import (
	"os/exec"
	"path/filepath"
	"strconv"
)

func resolveExecutable(configured string) (string, error) {
	name := configured
	if name == "" {
		name = "bwrap"
	}

	resolved, err := exec.LookPath(name)
	if err != nil {
		return "", err
	}
	if filepath.IsAbs(resolved) {
		return filepath.Clean(resolved), nil
	}
	return filepath.Abs(resolved)
}

func namespaceArgs(uid, gid int) []string {
	return []string{
		"--unshare-user",
		"--uid", strconv.Itoa(uid),
		"--gid", strconv.Itoa(gid),
		"--unshare-pid",
		"--unshare-ipc",
		"--unshare-uts",
		"--die-with-parent",
	}
}

const childExtraFileBaseFD = 3

func childFDPath(fd int) string {
	return "/proc/self/fd/" + strconv.Itoa(fd)
}
