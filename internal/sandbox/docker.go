package sandbox

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	pathpkg "path"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"petris.dev/toby/internal/config"
	"petris.dev/toby/internal/control"
	"petris.dev/toby/internal/executil"
	"petris.dev/toby/internal/exitcode"
	"petris.dev/toby/internal/tool"
)

const (
	DefaultDockerImage = "node:lts-bookworm"
	dockerRuntimeDir   = "/tmp/toby-runtime"
)

type DockerEnvironment struct {
	paths  config.Paths
	runner executil.Runner
}

type DockerInstance struct {
	baseInstance
	runner        executil.Runner
	image         string
	containerName string
	homeVolume    string
}

func NewDockerEnvironment(paths config.Paths, runner executil.Runner) *DockerEnvironment {
	return &DockerEnvironment{paths: paths, runner: runner}
}

func ProvideDockerEnvironment(paths config.Paths, runner executil.Runner) EnvironmentResult {
	return EnvironmentResult{Environment: NewDockerEnvironment(paths, runner)}
}

func (e *DockerEnvironment) Name() string { return RuntimeDocker }

func (e *DockerEnvironment) NewInstance(spec Spec) (Instance, error) {
	image := spec.DockerImage
	if image == "" {
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
	runtimeHostDir := filepath.Join(e.paths.XDGRuntimeDir, "toby", "docker", fmt.Sprintf("%d-%d", os.Getpid(), time.Now().UnixNano()))
	base := baseInstance{
		paths:                 e.paths,
		label:                 spec.Label,
		homeDir:               home,
		projectsDir:           projectsDir,
		runtimeDir:            dockerRuntimeDir,
		hostRuntimeDir:        runtimeHostDir,
		hostControlSocketPath: filepath.Join(runtimeHostDir, "toby", control.SandboxSocketName),
		workdir:               spec.Workdir,
		projects:              newProjectMounts(spec.Projects, projectsDir),
		tempRuntime:           runtimeHostDir,
	}
	return &DockerInstance{
		baseInstance:  base,
		runner:        e.runner,
		image:         image,
		containerName: fmt.Sprintf("toby-%d-%d", os.Getpid(), time.Now().UnixNano()),
		homeVolume:    dockerHomeVolumeName(spec.Label),
	}, nil
}

func (s *DockerInstance) Run(ctx context.Context, spec RunSpec) (int, error) {
	if err := s.prepareHostDirs(); err != nil {
		return 1, err
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
		_ = exec.Command("docker", "rm", "-f", s.containerName).Run()
	}
	return code, runErr
}

func (s *DockerInstance) BuildHomeVolumeInitCommand() []string {
	return []string{
		"docker", "run", "--rm",
		"--user", "0:0",
		"--entrypoint", "sh",
		"--mount", dockerVolume(s.homeVolume, s.HomeDir()),
		"--env", "HOME=" + s.HomeDir(),
		s.image,
		"-c", `set -e; mkdir -p "$1"; chown -R "$2:$3" "$1" 2>/dev/null || true; chmod -R u+rwX,go+rwX "$1"`,
		"sh", s.HomeDir(), strconv.Itoa(os.Getuid()), strconv.Itoa(os.Getgid()),
	}
}

func (s *DockerInstance) BuildCommand(spec RunSpec) ([]string, error) {
	tobyBinary, err := os.Executable()
	if err != nil {
		return nil, err
	}
	args := []string{"docker", "run", "--rm", "--init", "-i"}
	if stdinIsTerminal() && stdoutIsTerminal() {
		args = append(args, "-t")
	}
	args = append(args,
		"--name", s.containerName,
		"--network", "host",
		"--user", fmt.Sprintf("%d:%d", os.Getuid(), os.Getgid()),
	)
	groups, err := os.Getgroups()
	if err == nil {
		for _, group := range groups {
			args = append(args, "--group-add", strconv.Itoa(group))
		}
	}
	for _, mount := range s.mounts(tobyBinary, spec.Toolset) {
		args = append(args, "--mount", mount)
	}
	for _, item := range dockerEnv(spec.Env) {
		args = append(args, "--env", item)
	}
	args = append(args, "--workdir", s.chdirDir(), s.image)
	args = append(args, spec.Argv...)
	return args, nil
}

func (s *DockerInstance) mounts(tobyBinary string, toolset *tool.Toolset) []string {
	mounts := []string{
		dockerVolume(s.homeVolume, s.HomeDir()),
		dockerBind(s.hostRuntimeDir, s.runtimeDir, false),
		dockerBind(tobyBinary, s.TobyBinaryPath(), true),
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
