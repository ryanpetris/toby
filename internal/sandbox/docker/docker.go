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
	"petris.dev/toby/internal/control"
	"petris.dev/toby/internal/diagnostic/exitcode"
	"petris.dev/toby/internal/platform/executil"
	"petris.dev/toby/internal/sandbox"
	sandboxmount "petris.dev/toby/internal/sandbox/mount"
	sandboxpath "petris.dev/toby/internal/sandbox/path"
	"petris.dev/toby/internal/tools/helpers"
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
	primed        bool
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
	sandboxPaths := sandboxpath.Defaults()
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
		PathSet:            sandboxPaths,
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

func (s *instance) Prime(ctx context.Context, spec sandbox.RunSpec) (int, error) {
	if s.primed {
		return 0, nil
	}
	if code, err := s.resolveImage(ctx, spec); err != nil || code != 0 {
		return code, err
	}
	primeCmd := s.BuildPrimeCommand(spec)
	primeCode, primeErr := s.runner.Run(ctx, primeCmd, nil, executil.Options{HideOutput: spec.ExecOptions.HideOutput})
	if primeErr != nil {
		return primeCode, primeErr
	}
	if primeCode != 0 {
		return primeCode, exitcode.New(primeCode, "docker volume preparation failed")
	}
	s.primed = true
	return 0, nil
}

func (s *instance) Setup(ctx context.Context, spec sandbox.RunSpec) (int, error) {
	cmd, err := s.BuildSetupCommand(spec)
	if err != nil {
		return 1, err
	}
	code, runErr := s.runner.Run(ctx, cmd, nil, executil.Options{HideOutput: spec.ExecOptions.HideOutput})
	if ctx.Err() != nil && s.containerName != "" {
		_ = exec.Command(s.docker, "rm", "-f", s.containerName).Run()
	}
	return code, runErr
}

func (s *instance) Run(ctx context.Context, spec sandbox.RunSpec) (int, error) {
	if code, err := s.Prime(ctx, spec); err != nil || code != 0 {
		return code, err
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

func (s *instance) BuildPrimeCommand(spec sandbox.RunSpec) []string {
	args := []string{
		s.docker, "run", "--rm",
		"--user", "0:0",
		"--entrypoint", "/bin/sh",
	}
	for _, mount := range s.finalMounts(spec.Binds, spec.Mounts) {
		args = append(args, "--mount", mount)
	}
	args = append(args, "--workdir", s.ChdirDir(), s.image, "-c", "exit")
	return args
}

func (s *instance) BuildCommand(spec sandbox.RunSpec) ([]string, error) {
	args := []string{s.docker, "run", "--rm", "--init", "-i"}
	if stdinIsTerminal() && stdoutIsTerminal() {
		args = append(args, "-t")
	}
	args = append(args,
		"--name", s.containerName,
		"--user", "0:0",
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
	for _, mount := range s.finalMounts(spec.Binds, spec.Mounts) {
		args = append(args, "--mount", mount)
	}
	for _, item := range dockerControlEnv(spec.Env) {
		args = append(args, "--env", item)
	}
	args = append(args, "--workdir", s.ChdirDir(), s.image)
	args = append(args, spec.Argv...)
	return args, nil
}

func (s *instance) BuildSetupCommand(spec sandbox.RunSpec) ([]string, error) {
	args := []string{s.docker, "run", "--rm", "--init", "-i"}
	args = append(args,
		"--name", s.containerName,
		"--user", "0:0",
	)
	if runtime.GOOS != "darwin" {
		args = append(args, "--network", "host")
	}
	for _, mount := range s.setupMounts(spec.Mounts) {
		args = append(args, "--mount", mount)
	}
	for _, item := range dockerControlEnv(spec.Env) {
		args = append(args, "--env", item)
	}
	args = append(args, "--workdir", "/", s.image)
	args = append(args, spec.Argv...)
	return args, nil
}

func (s *instance) finalMounts(binds []sandboxmount.Bind, resolved []sandboxmount.RuntimeMount) []string {
	type finalMount struct {
		target string
		value  string
	}
	items := []finalMount{}
	for _, project := range s.ProjectMounts() {
		items = append(items, finalMount{target: project.SandboxPath, value: dockerBind(project.HostPath, project.SandboxPath, false)})
	}
	for _, bind := range binds {
		if bind.Optional {
			if _, err := os.Stat(bind.HostPath); err != nil {
				if errors.Is(err, os.ErrNotExist) {
					continue
				}
			}
		}
		target := helpers.ResolvePath(bind.Target, s)
		items = append(items, finalMount{target: target, value: dockerBind(bind.HostPath, target, bind.Access == sandboxmount.AccessReadOnly)})
	}
	for _, mount := range resolved {
		switch mount.Source.Kind {
		case sandboxmount.SourceProvider:
			items = append(items, finalMount{target: mount.Target, value: dockerVolume(mount.ProviderID, mount.Target)})
		case sandboxmount.SourceHostPath:
			items = append(items, finalMount{target: mount.Target, value: dockerBind(mount.Source.Value, mount.Target, mount.Access == sandboxmount.AccessReadOnly)})
		}
	}
	sort.SliceStable(items, func(i, j int) bool {
		return items[i].target < items[j].target
	})
	mounts := make([]string, 0, len(items))
	for _, item := range items {
		mounts = append(mounts, item.value)
	}
	return mounts
}

func (s *instance) setupMounts(resolved []sandboxmount.RuntimeMount) []string {
	mounts := []string{}
	for _, mount := range resolved {
		if mount.Source.Kind == sandboxmount.SourceProvider && mount.SetupPath != "" {
			mounts = append(mounts, dockerVolume(mount.ProviderID, mount.SetupPath))
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

func dockerEnv(env tool.Environment) []string {
	values := make([]string, 0, len(env))
	for name, value := range env {
		values = append(values, name+"="+value)
	}
	sort.Strings(values)
	return values
}

func dockerControlEnv(env tool.Environment) []string {
	controlEnv := tool.Environment{}
	if value, ok := env["HOME"]; ok {
		controlEnv["HOME"] = value
	}
	for _, name := range []string{control.EnvControlHost, control.EnvControlToken} {
		if value, ok := env[name]; ok {
			controlEnv[name] = value
		}
	}
	return dockerEnv(controlEnv)
}

func stdinIsTerminal() bool { return isCharDevice(os.Stdin) }

func stdoutIsTerminal() bool { return isCharDevice(os.Stdout) }

func isCharDevice(file *os.File) bool {
	info, err := file.Stat()
	return err == nil && info.Mode()&os.ModeCharDevice != 0
}
