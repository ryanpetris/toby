package docker

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"

	"petris.dev/toby/diagnostic/exitcode"
	"petris.dev/toby/internal/dirty/sandbox"
)

// resolveImage ensures s.image refers to a runnable image, building from the
// configured Dockerfile when needed. Image building still shells out to the
// `docker` CLI (BuildKit via the SDK is a much larger surface); running
// containers goes through testcontainers-go.
func (s *instance) resolveImage(ctx context.Context, spec sandbox.RunSpec) (int, error) {
	if !s.build.IsSet() {
		if s.image == "" {
			return 2, exitcode.New(2, "docker image is required")
		}
		return 0, nil
	}
	hide := spec.ExecOptions.HideOutput
	if s.image != "" {
		code, err := runDockerCLI(ctx, true, "image", "inspect", s.image)
		if err != nil {
			return code, err
		}
		if code == 0 {
			return 0, nil
		}
		code, err = runDockerCLI(ctx, hide, "build", "-t", s.image, "-f", s.build.Dockerfile, s.build.Context)
		if err != nil {
			return code, err
		}
		if code != 0 {
			return code, exitcode.New(code, "docker image build failed")
		}
		return 0, nil
	}
	iidFile, err := os.CreateTemp("", "toby-docker-image-*.iid")
	if err != nil {
		return 1, err
	}
	iidPath := iidFile.Name()
	if err := iidFile.Close(); err != nil {
		return 1, err
	}
	_ = os.Remove(iidPath)
	defer os.Remove(iidPath)
	code, err := runDockerCLI(ctx, hide, "build", "--iidfile", iidPath, "-f", s.build.Dockerfile, s.build.Context)
	if err != nil {
		return code, err
	}
	if code != 0 {
		return code, exitcode.New(code, "docker image build failed")
	}
	data, err := os.ReadFile(iidPath)
	if err != nil {
		return 1, err
	}
	image := strings.TrimSpace(string(data))
	if image == "" {
		return 1, fmt.Errorf("docker build did not write an image id")
	}
	s.image = image
	return 0, nil
}

func dockerCLI() string {
	if path, err := exec.LookPath("docker"); err == nil && path != "" {
		return path
	}
	return "docker"
}

func runDockerCLI(ctx context.Context, hideOutput bool, args ...string) (int, error) {
	cmd := exec.CommandContext(ctx, dockerCLI(), args...)
	if !hideOutput {
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
	}
	err := cmd.Run()
	if err == nil {
		return 0, nil
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		return exitErr.ExitCode(), nil
	}
	return 1, err
}
