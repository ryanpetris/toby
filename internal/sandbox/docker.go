package sandbox

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	pathpkg "path"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"time"

	"petris.dev/toby/internal/config"
	"petris.dev/toby/internal/executil"
	"petris.dev/toby/internal/exitcode"
	"petris.dev/toby/internal/tool"
)

const DefaultDockerImage = "node:lts-bookworm"

type DockerEnvironment struct {
	paths     config.Paths
	runner    executil.Runner
	docker    string
	available error
}

type DockerInstance struct {
	baseInstance
	runner        executil.Runner
	docker        string
	image         string
	build         tool.DockerBuildConfig
	containerName string
	homeVolume    string
}

func NewDockerEnvironment(paths config.Paths, runner executil.Runner) *DockerEnvironment {
	docker, err := exec.LookPath("docker")
	return newDockerEnvironment(paths, runner, docker, err)
}

func newDockerEnvironment(paths config.Paths, runner executil.Runner, docker string, available error) *DockerEnvironment {
	if docker == "" {
		docker = "docker"
	}
	return &DockerEnvironment{paths: paths, runner: runner, docker: docker, available: available}
}

func ProvideDockerEnvironment(paths config.Paths, runner executil.Runner) EnvironmentResult {
	return EnvironmentResult{Environment: NewDockerEnvironment(paths, runner)}
}

func (e *DockerEnvironment) Name() string { return RuntimeDocker }

func (e *DockerEnvironment) Priority() int { return 0 }

func (e *DockerEnvironment) Available() error { return e.available }

func (e *DockerEnvironment) NewInstance(spec Spec) (Instance, error) {
	image := spec.DockerImage
	if image == "" && !spec.DockerBuild.IsSet() {
		image = DefaultDockerImage
	}
	home := spec.DockerHome
	if home == "" {
		home = e.paths.Home
	} else {
		home = expandSandboxHome(home, e.paths.Home)
	}
	projectsDir := spec.DockerProjects
	if projectsDir == "" {
		projectsDir = e.paths.ProjectRoot
	} else {
		projectsDir = expandSandboxHome(projectsDir, home)
	}
	if !pathpkg.IsAbs(home) {
		return nil, exitcode.New(2, "docker home must be an absolute sandbox path: %s", home)
	}
	if !pathpkg.IsAbs(projectsDir) {
		return nil, exitcode.New(2, "docker projects must be an absolute sandbox path: %s", projectsDir)
	}
	token, err := newControlToken()
	if err != nil {
		return nil, err
	}
	sandboxControlHost := ""
	if runtime.GOOS == "darwin" {
		sandboxControlHost = "host.docker.internal"
	}
	base := baseInstance{
		paths:              e.paths,
		label:              spec.Label,
		homeDir:            home,
		projectsDir:        projectsDir,
		runtimeDir:         RuntimeDir,
		controlToken:       token,
		sandboxControlHost: sandboxControlHost,
		workdir:            spec.Workdir,
		projects:           newProjectMounts(spec.Projects, projectsDir),
	}
	return &DockerInstance{
		baseInstance:  base,
		runner:        e.runner,
		docker:        e.docker,
		image:         image,
		build:         spec.DockerBuild,
		containerName: fmt.Sprintf("toby-%d-%d", os.Getpid(), time.Now().UnixNano()),
		homeVolume:    dockerHomeVolumeName(spec.Label),
	}, nil
}

func (s *DockerInstance) Run(ctx context.Context, spec RunSpec) (int, error) {
	if code, err := s.resolveImage(ctx, spec); err != nil || code != 0 {
		return code, err
	}
	initCmd := s.BuildHomeVolumeInitCommand()
	initCode, initErr := s.runner.Run(ctx, initCmd, spec.Env, executil.Options{HideOutput: spec.ExecOptions.HideOutput})
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
	code, runErr := s.runner.Run(ctx, cmd, spec.Env, executil.Options{HideOutput: spec.ExecOptions.HideOutput})
	if ctx.Err() != nil && s.containerName != "" {
		_ = exec.Command(s.docker, "rm", "-f", s.containerName).Run()
	}
	return code, runErr
}

func (s *DockerInstance) resolveImage(ctx context.Context, spec RunSpec) (int, error) {
	if !s.build.IsSet() {
		if s.image == "" {
			return 2, exitcode.New(2, "docker image is required")
		}
		return 0, nil
	}
	if s.image != "" {
		inspectCode, inspectErr := s.runner.Run(ctx, s.BuildImageInspectCommand(), spec.Env, executil.Options{HideOutput: true})
		if inspectErr != nil {
			return inspectCode, inspectErr
		}
		if inspectCode == 0 {
			return 0, nil
		}
		buildCmd := s.BuildTaggedImageBuildCommand()
		buildCode, buildErr := s.runner.Run(ctx, buildCmd, spec.Env, executil.Options{HideOutput: spec.ExecOptions.HideOutput})
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
	buildCode, buildErr := s.runner.Run(ctx, buildCmd, spec.Env, executil.Options{HideOutput: spec.ExecOptions.HideOutput})
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

func (s *DockerInstance) BuildImageInspectCommand() []string {
	return []string{s.docker, "image", "inspect", s.image}
}

func (s *DockerInstance) BuildTaggedImageBuildCommand() []string {
	return []string{s.docker, "build", "-t", s.image, "-f", s.build.Dockerfile, s.build.Context}
}

func (s *DockerInstance) BuildUntaggedImageBuildCommand(iidFile string) []string {
	return []string{s.docker, "build", "--iidfile", iidFile, "-f", s.build.Dockerfile, s.build.Context}
}

func (s *DockerInstance) BuildHomeVolumeInitCommand() []string {
	return []string{
		s.docker, "run", "--rm",
		"--user", "0:0",
		"--entrypoint", "chown",
		"--mount", dockerVolume(s.homeVolume, s.HomeDir()),
		"--env", "HOME=" + s.HomeDir(),
		s.image,
		"-R", fmt.Sprintf("%d:%d", os.Getuid(), os.Getgid()), s.HomeDir(),
	}
}

func (s *DockerInstance) BuildCommand(spec RunSpec) ([]string, error) {
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
	args = append(args, "--workdir", s.chdirDir(), s.image)
	args = append(args, spec.Argv...)
	return args, nil
}

func (s *DockerInstance) mounts(toolset *tool.Toolset) []string {
	mounts := []string{
		dockerVolume(s.homeVolume, s.HomeDir()),
	}
	for _, project := range s.projects {
		mounts = append(mounts, dockerBind(project.hostPath, project.sandboxPath, false))
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
