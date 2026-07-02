// The launch runner is the tool container's main command: it reads the launch
// descriptor the daemon wrote (into the shared home volume), applies the environment,
// changes to the working directory, and execs the actual tool — replacing itself, so
// the client's attached PTY drives the tool directly. The container runs as the
// invoking user, so the exec runs as that user.

package sandbox

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
)

// LaunchDescriptor is what the daemon writes and the launch runner reads. Shared so
// both sides agree on the shape.
type LaunchDescriptor struct {
	Argv       []string `json:"argv"`
	Env        []string `json:"env"`
	WorkingDir string   `json:"workingDir"`
}

// LaunchRunner reads a descriptor and execs the tool.
type LaunchRunner struct{}

func NewLaunchRunner() *LaunchRunner { return &LaunchRunner{} }

// Run reads the descriptor at path and execs the tool. It does not return on success.
func (r *LaunchRunner) Run(path string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("read launch descriptor: %w", err)
	}
	var d LaunchDescriptor
	if err := json.Unmarshal(data, &d); err != nil {
		return fmt.Errorf("parse launch descriptor: %w", err)
	}
	if len(d.Argv) == 0 {
		return fmt.Errorf("launch descriptor has no argv")
	}

	bin, err := resolveBinary(d.Argv[0], d.Env)
	if err != nil {
		return err
	}
	if d.WorkingDir != "" {
		if err := os.Chdir(d.WorkingDir); err != nil {
			return fmt.Errorf("chdir %s: %w", d.WorkingDir, err)
		}
	}
	return syscall.Exec(bin, d.Argv, d.Env)
}

// resolveBinary finds argv0 either as a path or by searching PATH from the descriptor's
// environment.
func resolveBinary(argv0 string, env []string) (string, error) {
	if strings.Contains(argv0, "/") {
		return argv0, nil
	}
	pathEnv := ""
	for _, kv := range env {
		if strings.HasPrefix(kv, "PATH=") {
			pathEnv = kv[len("PATH="):]
		}
	}
	// Point os PATH at the descriptor's so exec.LookPath resolves against it.
	old := os.Getenv("PATH")
	_ = os.Setenv("PATH", pathEnv)
	defer os.Setenv("PATH", old)
	if p, err := exec.LookPath(argv0); err == nil {
		return p, nil
	}
	for _, dir := range filepath.SplitList(pathEnv) {
		candidate := filepath.Join(dir, argv0)
		if info, err := os.Stat(candidate); err == nil && !info.IsDir() {
			return candidate, nil
		}
	}
	return "", fmt.Errorf("tool %q not found on PATH", argv0)
}
