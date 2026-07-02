package runtime

// Image building: resolveImage builds the configured Dockerfile via the `docker`
// CLI when the target image is missing, since driving BuildKit through the SDK
// is a much larger surface than running containers.

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"

	"petris.dev/toby/diagnostic/exitcode"
	"petris.dev/toby/tools"
)

// EnsureImage resolves spec to a runnable image reference, building from the
// configured Dockerfile when the target image is missing. Image building shells out
// to the `docker` CLI (BuildKit via the SDK is a much larger surface); running
// containers goes through testcontainers-go.
func EnsureImage(ctx context.Context, spec Spec, hideOutput bool) (string, error) {
	if !spec.Build.IsSet() {
		image := spec.ResolvedImage()
		if image == "" {
			return "", exitcode.New(2, "docker image is required")
		}
		return image, nil
	}
	image, code, err := BuildImage(ctx, spec.Build, spec.Image, hideOutput)
	if err != nil {
		return "", err
	}
	if code != 0 {
		return "", exitcode.New(code, "docker image preparation failed")
	}
	return image, nil
}

// BuildImage builds the Dockerfile context described by build (which must be set)
// via the `docker` CLI and returns a runnable image reference. When image is
// non-empty it is used as the build tag and an already-present image is reused
// without rebuilding; when empty, the built image id is captured with --iidfile
// and returned. A non-zero docker exit is returned as the code with an error.
func BuildImage(ctx context.Context, build tools.Build, image string, hideOutput bool) (string, int, error) {
	if image != "" {
		code, err := runDockerCLI(ctx, true, "image", "inspect", image)
		if err != nil {
			return "", code, err
		}
		if code == 0 {
			return image, 0, nil
		}
		code, err = runDockerCLI(ctx, hideOutput, "build", "-t", image, "-f", build.Dockerfile, build.Context)
		if err != nil {
			return "", code, err
		}
		if code != 0 {
			return "", code, exitcode.New(code, "docker image build failed")
		}
		return image, 0, nil
	}
	iidFile, err := os.CreateTemp("", "toby-docker-image-*.iid")
	if err != nil {
		return "", 1, err
	}
	iidPath := iidFile.Name()
	if err := iidFile.Close(); err != nil {
		return "", 1, err
	}
	_ = os.Remove(iidPath)
	defer os.Remove(iidPath)
	code, err := runDockerCLI(ctx, hideOutput, "build", "--iidfile", iidPath, "-f", build.Dockerfile, build.Context)
	if err != nil {
		return "", code, err
	}
	if code != 0 {
		return "", code, exitcode.New(code, "docker image build failed")
	}
	data, err := os.ReadFile(iidPath)
	if err != nil {
		return "", 1, err
	}
	built := strings.TrimSpace(string(data))
	if built == "" {
		return "", 1, fmt.Errorf("docker build did not write an image id")
	}
	return built, 0, nil
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
