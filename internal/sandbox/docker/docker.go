package docker

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	pathpkg "path"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"time"

	"petris.dev/toby/internal/config"
	"petris.dev/toby/internal/diagnostic/exitcode"
	"petris.dev/toby/internal/platform/executil"
	"petris.dev/toby/internal/sandbox"
	"petris.dev/toby/internal/tools/tool"

	"go.uber.org/fx"
)

const defaultDockerImage = "node:lts-bookworm"

type environment struct {
	paths     config.Paths
	runner    executil.Runner
	docker    string
	available error
}

type instance struct {
	sandbox.BaseInstance
	runner        executil.Runner
	docker        string
	image         string
	build         tool.DockerBuildConfig
	containerName string
	homeVolume    string
}

func Module() fx.Option {
	return fx.Module(
		"sandbox.docker",
		fx.Provide(fx.Annotate(
			newEnvironment,
			fx.ResultTags(`group:"`+sandbox.FxEnvironmentGroup+`"`),
		)),
	)
}

func newEnvironment(paths config.Paths, runner executil.Runner) sandbox.Environment {
	docker, err := exec.LookPath("docker")
	return newDockerEnvironment(paths, runner, docker, err)
}

func newDockerEnvironment(paths config.Paths, runner executil.Runner, docker string, available error) *environment {
	if docker == "" {
		docker = "docker"
	}
	return &environment{paths: paths, runner: runner, docker: docker, available: available}
}

func (e *environment) Name() string { return sandbox.RuntimeDocker }

func (e *environment) Priority() int { return 0 }

func (e *environment) Available() error { return e.available }

func (e *environment) NewInstance(spec sandbox.Spec) (sandbox.Instance, error) {
	image := spec.DockerImage
	if image == "" && !spec.DockerBuild.IsSet() {
		image = defaultDockerImage
	}
	sandboxPaths := tool.DefaultSandboxPaths()
	if spec.DockerHome != "" {
		sandboxPaths.Home = expandSandboxHome(spec.DockerHome, sandboxPaths.Home)
	}
	if spec.DockerProjects != "" {
		sandboxPaths.Workspace = expandSandboxHome(spec.DockerProjects, sandboxPaths.Home)
	}
	if !pathpkg.IsAbs(sandboxPaths.Home) {
		return nil, exitcode.New(2, "docker home must be an absolute sandbox path: %s", sandboxPaths.Home)
	}
	if !pathpkg.IsAbs(sandboxPaths.Workspace) {
		return nil, exitcode.New(2, "docker projects must be an absolute sandbox path: %s", sandboxPaths.Workspace)
	}
	if err := validateSandboxPathUnderRoot("docker home", sandboxPaths.Home, sandboxPaths.Root); err != nil {
		return nil, exitcode.New(2, "%s", err)
	}
	if err := validateSandboxPathUnderRoot("docker projects", sandboxPaths.Workspace, sandboxPaths.Root); err != nil {
		return nil, exitcode.New(2, "%s", err)
	}
	sandboxControlHost := ""
	if runtime.GOOS == "darwin" {
		sandboxControlHost = "host.docker.internal"
	}
	base, err := sandbox.NewBaseInstance(sandbox.BaseInstanceParams{
		Paths:              e.paths,
		Label:              spec.Label,
		SandboxPaths:       sandboxPaths,
		HomeDir:            sandboxPaths.Home,
		ProjectsDir:        sandboxPaths.Workspace,
		RuntimeDir:         sandboxPaths.Root,
		SandboxControlHost: sandboxControlHost,
		Workdir:            spec.Workdir,
		Projects:           spec.Projects,
	})
	if err != nil {
		return nil, err
	}
	return &instance{
		BaseInstance:  base,
		runner:        e.runner,
		docker:        e.docker,
		image:         image,
		build:         spec.DockerBuild,
		containerName: fmt.Sprintf("toby-%d-%d", os.Getpid(), time.Now().UnixNano()),
		homeVolume:    dockerHomeVolumeName(spec.Label),
	}, nil
}

func validateSandboxPathUnderRoot(label, path, root string) error {
	path = pathpkg.Clean(filepath.ToSlash(path))
	root = pathpkg.Clean(filepath.ToSlash(root))
	if path == root || strings.HasPrefix(path, strings.TrimRight(root, "/")+"/") {
		return nil
	}
	return fmt.Errorf("%s must be under sandbox root %s: %s", label, root, path)
}

func expandSandboxHome(value, home string) string {
	if value == "~" {
		return home
	}
	if strings.HasPrefix(value, "~/") {
		return filepath.Join(home, filepath.FromSlash(strings.TrimPrefix(value, "~/")))
	}
	return value
}

func (s *instance) Run(ctx context.Context, spec sandbox.RunSpec) (int, error) {
	if code, err := s.resolveImage(ctx, spec); err != nil || code != 0 {
		return code, err
	}
	primeCmd := s.BuildHomeVolumePrimeCommand(spec)
	primeCode, primeErr := s.runner.Run(ctx, primeCmd, nil, executil.Options{HideOutput: spec.ExecOptions.HideOutput})
	if primeErr != nil {
		return primeCode, primeErr
	}
	if primeCode != 0 {
		return primeCode, exitcode.New(primeCode, "docker home volume preparation failed")
	}
	initCmd := s.BuildHomeVolumeInitCommand()
	initCode, initErr := s.runner.Run(ctx, initCmd, nil, executil.Options{HideOutput: spec.ExecOptions.HideOutput})
	if initErr != nil {
		return initCode, initErr
	}
	if initCode != 0 {
		return initCode, exitcode.New(initCode, "docker home volume initialization failed")
	}
	cmd, err := s.BuildCommand(spec)
	if err != nil {
		return 1, err
	}
	code, runErr := s.runner.Run(ctx, cmd, nil, executil.Options{HideOutput: spec.ExecOptions.HideOutput})
	if ctx.Err() != nil && s.containerName != "" {
		_ = exec.Command(s.docker, "rm", "-f", s.containerName).Run()
	}
	return code, runErr
}

func (s *instance) resolveImage(ctx context.Context, spec sandbox.RunSpec) (int, error) {
	if !s.build.IsSet() {
		if s.image == "" {
			return 2, exitcode.New(2, "docker image is required")
		}
		return 0, nil
	}
	if s.image != "" {
		inspectCode, inspectErr := s.runner.Run(ctx, s.BuildImageInspectCommand(), nil, executil.Options{HideOutput: true})
		if inspectErr != nil {
			return inspectCode, inspectErr
		}
		if inspectCode == 0 {
			return 0, nil
		}
		buildCmd := s.BuildTaggedImageBuildCommand()
		buildCode, buildErr := s.runner.Run(ctx, buildCmd, nil, executil.Options{HideOutput: spec.ExecOptions.HideOutput})
		if buildErr != nil {
			return buildCode, buildErr
		}
		if buildCode != 0 {
			return buildCode, exitcode.New(buildCode, "docker image build failed")
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
	buildCmd := s.BuildUntaggedImageBuildCommand(iidPath)
	buildCode, buildErr := s.runner.Run(ctx, buildCmd, nil, executil.Options{HideOutput: spec.ExecOptions.HideOutput})
	if buildErr != nil {
		return buildCode, buildErr
	}
	if buildCode != 0 {
		return buildCode, exitcode.New(buildCode, "docker image build failed")
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

func (s *instance) BuildImageInspectCommand() []string {
	return []string{s.docker, "image", "inspect", s.image}
}

func (s *instance) BuildTaggedImageBuildCommand() []string {
	return []string{s.docker, "build", "-t", s.image, "-f", s.build.Dockerfile, s.build.Context}
}

func (s *instance) BuildUntaggedImageBuildCommand(iidFile string) []string {
	return []string{s.docker, "build", "--iidfile", iidFile, "-f", s.build.Dockerfile, s.build.Context}
}

func (s *instance) BuildHomeVolumePrimeCommand(spec sandbox.RunSpec) []string {
	args := []string{
		s.docker, "run", "--rm",
		"--user", "0:0",
		"--entrypoint", "/bin/sh",
	}
	for _, mount := range s.mounts(spec.Toolset) {
		args = append(args, "--mount", mount)
	}
	args = append(args, "--workdir", s.ChdirDir(), s.image, "-c", "exit")
	return args
}

func (s *instance) BuildHomeVolumeInitCommand() []string {
	return []string{
		s.docker, "run", "--rm",
		"--user", "0:0",
		"--entrypoint", "chown",
		"--mount", dockerVolume(s.homeVolume, s.TobyRuntimeDir()),
		"--env", "HOME=" + s.HomeDir(),
		s.image,
		"-R", fmt.Sprintf("%d:%d", os.Getuid(), os.Getgid()), s.TobyRuntimeDir(),
	}
}

func (s *instance) BuildCommand(spec sandbox.RunSpec) ([]string, error) {
	args := []string{s.docker, "run", "--rm", "--init", "-i"}
	if stdinIsTerminal() && stdoutIsTerminal() {
		args = append(args, "-t")
	}
	args = append(args,
		"--name", s.containerName,
		"--user", fmt.Sprintf("%d:%d", os.Getuid(), os.Getgid()),
	)
	if runtime.GOOS != "darwin" {
		args = append(args, "--network", "host")
	}
	groups, err := os.Getgroups()
	if err == nil {
		for _, group := range groups {
			args = append(args, "--group-add", strconv.Itoa(group))
		}
	}
	for _, mount := range s.mounts(spec.Toolset) {
		args = append(args, "--mount", mount)
	}
	for _, item := range dockerEnv(spec.Env) {
		args = append(args, "--env", item)
	}
	args = append(args, "--workdir", s.ChdirDir(), s.image)
	args = append(args, spec.Argv...)
	return args, nil
}

func (s *instance) mounts(toolset *tool.Toolset) []string {
	mounts := []string{
		dockerVolume(s.homeVolume, s.TobyRuntimeDir()),
	}
	for _, project := range s.ProjectMounts() {
		mounts = append(mounts, dockerBind(project.HostPath, project.SandboxPath, false))
	}
	if toolset != nil {
		for _, bind := range toolset.Binds() {
			if bind.Optional {
				if _, err := os.Stat(bind.HostPath); err != nil {
					if errors.Is(err, os.ErrNotExist) {
						continue
					}
				}
			}
			mounts = append(mounts, dockerBind(bind.HostPath, tool.ResolvePath(bind.Target, s), bind.Type == tool.BindReadOnly))
		}
	}
	return mounts
}

func dockerBind(source, target string, readonly bool) string {
	value := "type=bind,source=" + source + ",target=" + target
	if readonly {
		value += ",readonly"
	}
	return value
}

func dockerVolume(source, target string) string {
	return "type=volume,source=" + source + ",target=" + target
}

func dockerHomeVolumeName(label string) string {
	var b strings.Builder
	b.WriteString("toby-home-")
	lastDash := false
	for _, r := range label {
		if isDockerVolumeNameChar(r) {
			b.WriteRune(r)
			lastDash = false
			continue
		}
		if !lastDash {
			b.WriteByte('-')
			lastDash = true
		}
	}
	name := strings.TrimRight(b.String(), "-")
	if name == "toby-home" {
		return "toby-home-sandbox"
	}
	return name
}

func isDockerVolumeNameChar(r rune) bool {
	return r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '_' || r == '.' || r == '-'
}

func dockerEnv(env tool.Environment) []string {
	values := make([]string, 0, len(env))
	for name, value := range env {
		values = append(values, name+"="+value)
	}
	sort.Strings(values)
	return values
}

func stdinIsTerminal() bool { return isCharDevice(os.Stdin) }

func stdoutIsTerminal() bool { return isCharDevice(os.Stdout) }

func isCharDevice(file *os.File) bool {
	info, err := file.Stat()
	return err == nil && info.Mode()&os.ModeCharDevice != 0
}
