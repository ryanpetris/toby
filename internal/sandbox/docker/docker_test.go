package docker

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"

	"petris.dev/toby/internal/config"
	"petris.dev/toby/internal/platform/executil"
	"petris.dev/toby/internal/sandbox"
	"petris.dev/toby/internal/tools/helpers"
	"petris.dev/toby/internal/tools/tool"
)

type fakeRunner struct{}

func (fakeRunner) Run(context.Context, []string, map[string]string, executil.Options) (int, error) {
	return 0, nil
}

type recordingRunner struct {
	commands  [][]string
	envs      []map[string]string
	exitCodes []int
	iidImage  string
}

func (r *recordingRunner) Run(_ context.Context, argv []string, env map[string]string, _ executil.Options) (int, error) {
	r.commands = append(r.commands, append([]string(nil), argv...))
	r.envs = append(r.envs, cloneTestEnv(env))
	if r.iidImage != "" {
		for i, arg := range argv {
			if arg == "--iidfile" && i+1 < len(argv) {
				if err := os.WriteFile(argv[i+1], []byte(r.iidImage), 0o600); err != nil {
					return 1, err
				}
			}
		}
	}
	if index := len(r.commands) - 1; index < len(r.exitCodes) {
		return r.exitCodes[index], nil
	}
	return 0, nil
}

func cloneTestEnv(env map[string]string) map[string]string {
	if env == nil {
		return nil
	}
	clone := make(map[string]string, len(env))
	for name, value := range env {
		clone[name] = value
	}
	return clone
}

func TestDockerBuildCommandMountsHomeProjectsAndUsesDefaultImage(t *testing.T) {
	home := t.TempDir()
	paths := testPaths(home)
	projectDir := filepath.Join(paths.ProjectRoot, "demo")
	if err := os.MkdirAll(projectDir, 0o755); err != nil {
		t.Fatal(err)
	}
	factory := testFactory(paths, fakeRunner{})
	sbx, err := factory.FromOptions(&tool.CommandOptions{Env: "demo", SandboxRuntime: sandbox.RuntimeDocker})
	if err != nil {
		t.Fatal(err)
	}
	docker := sbx.(*instance)
	env := tool.Environment{"TOBY_CONTROL_HOST": "127.0.0.1:1234", "TOBY_CONTROL_TOKEN": "secret", "HOME": docker.HomeDir()}
	cmd, err := docker.BuildCommand(sandbox.RunSpec{Argv: []string{docker.TobyBinaryPath(), "sandbox", "manager"}, Env: env})
	if err != nil {
		t.Fatal(err)
	}
	assertContainsSequence(t, cmd, []string{"docker", "run", "--rm", "--init", "-i"})
	assertContainsSequence(t, cmd, []string{"--user", "0:0"})
	assertContainsSequence(t, cmd, []string{"--network", "host"})
	assertContainsSequence(t, cmd, []string{"--mount", dockerVolume("toby-home-demo", tool.DefaultSandboxHome)})
	assertContainsSequence(t, cmd, []string{"--mount", dockerBind(projectDir, filepath.Join(tool.DefaultSandboxWorkspace, "demo"), false)})
	assertContainsSequence(t, cmd, []string{"--env", "TOBY_CONTROL_HOST=127.0.0.1:1234"})
	assertContainsSequence(t, cmd, []string{"--env", "TOBY_CONTROL_TOKEN=secret"})
	assertContainsSequence(t, cmd, []string{"--env", "HOME=" + tool.DefaultSandboxHome})
	assertNoDockerEnv(t, cmd, "PATH")
	assertContainsSequence(t, cmd, []string{"--workdir", filepath.Join(tool.DefaultSandboxWorkspace, "demo"), defaultDockerImage})
	assertContainsSequence(t, cmd, []string{docker.TobyBinaryPath(), "sandbox", "manager"})
	primeCmd := docker.BuildHomeVolumePrimeCommand(sandbox.RunSpec{})
	assertContainsSequence(t, primeCmd, []string{"docker", "run", "--rm", "--user", "0:0", "--entrypoint", "/bin/sh"})
	assertContainsSequence(t, primeCmd, []string{"--mount", dockerVolume("toby-home-demo", tool.DefaultSandboxHome)})
	assertContainsSequence(t, primeCmd, []string{"--mount", dockerBind(projectDir, filepath.Join(tool.DefaultSandboxWorkspace, "demo"), false)})
	assertContainsSequence(t, primeCmd, []string{"--workdir", filepath.Join(tool.DefaultSandboxWorkspace, "demo"), defaultDockerImage, "-c", "exit"})
	initCmd := docker.BuildHomeVolumeInitCommand()
	assertContainsSequence(t, initCmd, []string{"docker", "run", "--rm", "--user", "0:0", "--entrypoint", "chown"})
	assertContainsSequence(t, initCmd, []string{"--mount", dockerVolume("toby-home-demo", tool.DefaultSandboxHome)})
	assertNoDockerEnv(t, initCmd, "HOME")
	assertContainsSequence(t, initCmd, []string{defaultDockerImage, "-R", strconv.Itoa(os.Getuid()) + ":" + strconv.Itoa(os.Getgid()), tool.DefaultSandboxHome})
}

func TestDockerRunInitializesHomeVolumeBeforeManager(t *testing.T) {
	home := t.TempDir()
	paths := testPaths(home)
	projectDir := filepath.Join(paths.ProjectRoot, "demo")
	if err := os.MkdirAll(projectDir, 0o755); err != nil {
		t.Fatal(err)
	}
	runner := &recordingRunner{}
	factory := testFactory(paths, runner)
	sbx, err := factory.FromOptions(&tool.CommandOptions{Env: "demo", SandboxRuntime: sandbox.RuntimeDocker})
	if err != nil {
		t.Fatal(err)
	}
	docker := sbx.(*instance)
	code, err := docker.Run(context.Background(), sandbox.RunSpec{Argv: []string{docker.TobyBinaryPath(), "sandbox", "manager"}, Env: tool.Environment{}})
	if err != nil || code != 0 {
		t.Fatalf("Run = %d, %v", code, err)
	}
	if len(runner.commands) != 3 {
		t.Fatalf("commands = %#v", runner.commands)
	}
	assertContainsSequence(t, runner.commands[0], []string{"--entrypoint", "/bin/sh"})
	assertContainsSequence(t, runner.commands[0], []string{"--mount", dockerVolume("toby-home-demo", tool.DefaultSandboxHome)})
	assertContainsSequence(t, runner.commands[0], []string{"--workdir", filepath.Join(tool.DefaultSandboxWorkspace, "demo"), defaultDockerImage, "-c", "exit"})
	assertContainsSequence(t, runner.commands[1], []string{"--entrypoint", "chown"})
	assertContainsSequence(t, runner.commands[1], []string{"--mount", dockerVolume("toby-home-demo", tool.DefaultSandboxHome)})
	assertContainsSequence(t, runner.commands[2], []string{"docker", "run", "--rm", "--init", "-i"})
}

func TestDockerRunBuildsTaggedImageWhenMissing(t *testing.T) {
	home := t.TempDir()
	paths := testPaths(home)
	projectDir := filepath.Join(paths.ProjectRoot, "demo")
	if err := os.MkdirAll(projectDir, 0o755); err != nil {
		t.Fatal(err)
	}
	contextDir := filepath.Join(home, "docker")
	if err := os.MkdirAll(contextDir, 0o755); err != nil {
		t.Fatal(err)
	}
	runner := &recordingRunner{exitCodes: []int{1, 0, 0, 0}}
	factory := testFactory(paths, runner)
	sbx, err := factory.FromOptions(&tool.CommandOptions{Env: "demo", SandboxRuntime: sandbox.RuntimeDocker, DockerImage: "custom:dev", DockerBuild: tool.DockerBuildConfig{Context: contextDir, Dockerfile: filepath.Join(contextDir, "Dockerfile.toby")}})
	if err != nil {
		t.Fatal(err)
	}
	docker := sbx.(*instance)
	code, err := docker.Run(context.Background(), sandbox.RunSpec{Argv: []string{"true"}, Env: tool.Environment{}})
	if err != nil || code != 0 {
		t.Fatalf("Run = %d, %v", code, err)
	}
	if len(runner.commands) != 5 {
		t.Fatalf("commands = %#v", runner.commands)
	}
	assertContainsSequence(t, runner.commands[0], []string{"docker", "image", "inspect", "custom:dev"})
	assertContainsSequence(t, runner.commands[1], []string{"docker", "build", "-t", "custom:dev", "-f", filepath.Join(contextDir, "Dockerfile.toby"), contextDir})
	assertContainsSequence(t, runner.commands[2], []string{"--entrypoint", "/bin/sh"})
	assertContainsSequence(t, runner.commands[3], []string{"custom:dev", "-R"})
	assertContainsSequence(t, runner.commands[4], []string{"--workdir", filepath.Join(tool.DefaultSandboxWorkspace, "demo"), "custom:dev"})
}

func TestDockerRunSkipsBuildWhenTaggedImageExists(t *testing.T) {
	home := t.TempDir()
	paths := testPaths(home)
	projectDir := filepath.Join(paths.ProjectRoot, "demo")
	if err := os.MkdirAll(projectDir, 0o755); err != nil {
		t.Fatal(err)
	}
	contextDir := filepath.Join(home, "docker")
	if err := os.MkdirAll(contextDir, 0o755); err != nil {
		t.Fatal(err)
	}
	runner := &recordingRunner{}
	factory := testFactory(paths, runner)
	sbx, err := factory.FromOptions(&tool.CommandOptions{Env: "demo", SandboxRuntime: sandbox.RuntimeDocker, DockerImage: "custom:dev", DockerBuild: tool.DockerBuildConfig{Context: contextDir, Dockerfile: filepath.Join(contextDir, "Dockerfile")}})
	if err != nil {
		t.Fatal(err)
	}
	docker := sbx.(*instance)
	code, err := docker.Run(context.Background(), sandbox.RunSpec{Argv: []string{"true"}, Env: tool.Environment{}})
	if err != nil || code != 0 {
		t.Fatalf("Run = %d, %v", code, err)
	}
	if len(runner.commands) != 4 {
		t.Fatalf("commands = %#v", runner.commands)
	}
	assertContainsSequence(t, runner.commands[0], []string{"docker", "image", "inspect", "custom:dev"})
	assertContainsSequence(t, runner.commands[1], []string{"--entrypoint", "/bin/sh"})
	assertContainsSequence(t, runner.commands[2], []string{"custom:dev", "-R"})
	assertContainsSequence(t, runner.commands[3], []string{"--workdir", filepath.Join(tool.DefaultSandboxWorkspace, "demo"), "custom:dev"})
}

func TestDockerRunBuildsUntaggedImageEveryTime(t *testing.T) {
	home := t.TempDir()
	paths := testPaths(home)
	projectDir := filepath.Join(paths.ProjectRoot, "demo")
	if err := os.MkdirAll(projectDir, 0o755); err != nil {
		t.Fatal(err)
	}
	contextDir := filepath.Join(home, "docker")
	if err := os.MkdirAll(contextDir, 0o755); err != nil {
		t.Fatal(err)
	}
	runner := &recordingRunner{iidImage: "sha256:built"}
	factory := testFactory(paths, runner)
	sbx, err := factory.FromOptions(&tool.CommandOptions{Env: "demo", SandboxRuntime: sandbox.RuntimeDocker, DockerBuild: tool.DockerBuildConfig{Context: contextDir, Dockerfile: filepath.Join(contextDir, "Dockerfile")}})
	if err != nil {
		t.Fatal(err)
	}
	docker := sbx.(*instance)
	code, err := docker.Run(context.Background(), sandbox.RunSpec{Argv: []string{"true"}, Env: tool.Environment{}})
	if err != nil || code != 0 {
		t.Fatalf("Run = %d, %v", code, err)
	}
	if len(runner.commands) != 4 {
		t.Fatalf("commands = %#v", runner.commands)
	}
	assertContainsSequence(t, runner.commands[0], []string{"docker", "build", "--iidfile"})
	assertContainsSequence(t, runner.commands[0], []string{"-f", filepath.Join(contextDir, "Dockerfile"), contextDir})
	assertContainsSequence(t, runner.commands[1], []string{"--entrypoint", "/bin/sh"})
	assertContainsSequence(t, runner.commands[2], []string{"sha256:built", "-R"})
	assertContainsSequence(t, runner.commands[3], []string{"--workdir", filepath.Join(tool.DefaultSandboxWorkspace, "demo"), "sha256:built"})
}

func TestDockerRunUsesHostEnvironmentForDockerCLI(t *testing.T) {
	home := t.TempDir()
	paths := testPaths(home)
	projectDir := filepath.Join(paths.ProjectRoot, "demo")
	if err := os.MkdirAll(projectDir, 0o755); err != nil {
		t.Fatal(err)
	}
	contextDir := filepath.Join(home, "docker")
	if err := os.MkdirAll(contextDir, 0o755); err != nil {
		t.Fatal(err)
	}
	runner := &recordingRunner{iidImage: "sha256:built"}
	factory := testFactory(paths, runner)
	sbx, err := factory.FromOptions(&tool.CommandOptions{Env: "demo", SandboxRuntime: sandbox.RuntimeDocker, DockerBuild: tool.DockerBuildConfig{Context: contextDir, Dockerfile: filepath.Join(contextDir, "Dockerfile")}})
	if err != nil {
		t.Fatal(err)
	}
	docker := sbx.(*instance)
	env := tool.Environment{"TOBY_CONTROL_HOST": "127.0.0.1:1234", "TOBY_CONTROL_TOKEN": "secret", "HOME": tool.DefaultSandboxHome}
	code, err := docker.Run(context.Background(), sandbox.RunSpec{Argv: []string{"true"}, Env: env})
	if err != nil || code != 0 {
		t.Fatalf("Run = %d, %v", code, err)
	}
	if len(runner.envs) != len(runner.commands) {
		t.Fatalf("envs = %#v, commands = %#v", runner.envs, runner.commands)
	}
	for i, got := range runner.envs {
		if got != nil {
			t.Fatalf("docker command %d env = %#v, want host env", i, got)
		}
	}
	assertContainsSequence(t, runner.commands[0], []string{"docker", "build", "--iidfile"})
	finalCommand := runner.commands[len(runner.commands)-1]
	assertContainsSequence(t, finalCommand, []string{"--env", "TOBY_CONTROL_HOST=127.0.0.1:1234"})
	assertContainsSequence(t, finalCommand, []string{"--env", "TOBY_CONTROL_TOKEN=secret"})
	assertContainsSequence(t, finalCommand, []string{"--env", "HOME=" + tool.DefaultSandboxHome})
	assertNotContainsSequence(t, finalCommand, []string{"--env", "TOBY_BIN_DIR=" + tool.DefaultSandboxBin})
	assertNotContainsSequence(t, finalCommand, []string{"--env", "TOBY_CONTEXT_DIR=" + filepath.Join(tool.DefaultSandboxRoot, "context")})
}

func TestDockerPrimeCommandUsesFinalMountsAndWorkdir(t *testing.T) {
	home := t.TempDir()
	paths := testPaths(home)
	projectDir := filepath.Join(paths.ProjectRoot, "demo")
	if err := os.MkdirAll(projectDir, 0o755); err != nil {
		t.Fatal(err)
	}
	factory := testFactory(paths, fakeRunner{})
	sbx, err := factory.FromOptions(&tool.CommandOptions{Env: "demo", SandboxRuntime: sandbox.RuntimeDocker})
	if err != nil {
		t.Fatal(err)
	}
	docker := sbx.(*instance)
	binds := []tool.Bind{{HostPath: "/host/opencode", Target: helpers.HomeTarget(".local", "share", "opencode"), Type: tool.BindRegular}}
	cmd := docker.BuildHomeVolumePrimeCommand(sandbox.RunSpec{Binds: binds})
	assertContainsSequence(t, cmd, []string{"docker", "run", "--rm", "--user", "0:0", "--entrypoint", "/bin/sh"})
	assertContainsSequence(t, cmd, []string{"--mount", dockerVolume("toby-home-demo", tool.DefaultSandboxHome)})
	assertContainsSequence(t, cmd, []string{"--mount", dockerBind(projectDir, filepath.Join(tool.DefaultSandboxWorkspace, "demo"), false)})
	assertContainsSequence(t, cmd, []string{"--mount", dockerBind("/host/opencode", filepath.Join(tool.DefaultSandboxHome, ".local", "share", "opencode"), false)})
	assertContainsSequence(t, cmd, []string{"--workdir", filepath.Join(tool.DefaultSandboxWorkspace, "demo"), defaultDockerImage, "-c", "exit"})
}

func TestDockerOptionsOverrideHomeProjectsAndImage(t *testing.T) {
	home := t.TempDir()
	paths := testPaths(home)
	projectDir := filepath.Join(paths.ProjectRoot, "demo")
	if err := os.MkdirAll(projectDir, 0o755); err != nil {
		t.Fatal(err)
	}
	factory := testFactory(paths, fakeRunner{})
	sbx, err := factory.FromOptions(&tool.CommandOptions{
		Env:            "demo",
		SandboxRuntime: sandbox.RuntimeDocker,
		DockerImage:    "custom:latest",
		DockerHome:     "/toby/custom-home",
		DockerProjects: "~/workspace",
	})
	if err != nil {
		t.Fatal(err)
	}
	docker := sbx.(*instance)
	if docker.HomeDir() != "/toby/custom-home" {
		t.Fatalf("HomeDir = %q", docker.HomeDir())
	}
	if docker.Projects() != "/toby/custom-home/workspace" {
		t.Fatalf("Projects = %q", docker.Projects())
	}
	cmd, err := docker.BuildCommand(sandbox.RunSpec{Argv: []string{"true"}, Env: tool.Environment{}})
	if err != nil {
		t.Fatal(err)
	}
	assertContainsSequence(t, cmd, []string{"--mount", dockerVolume("toby-home-demo", "/toby/custom-home")})
	assertContainsSequence(t, cmd, []string{"--workdir", "/toby/custom-home/workspace/demo", "custom:latest"})
}

func TestDockerOptionsRejectPathsOutsideSandboxRoot(t *testing.T) {
	home := t.TempDir()
	paths := testPaths(home)
	projectDir := filepath.Join(paths.ProjectRoot, "demo")
	if err := os.MkdirAll(projectDir, 0o755); err != nil {
		t.Fatal(err)
	}
	factory := testFactory(paths, fakeRunner{})
	for _, opts := range []tool.CommandOptions{
		{Env: "demo", SandboxRuntime: sandbox.RuntimeDocker, DockerHome: "/home/toby"},
		{Env: "demo", SandboxRuntime: sandbox.RuntimeDocker, DockerProjects: "/toby/../workspace"},
	} {
		if _, err := factory.FromOptions(&opts); err == nil {
			t.Fatalf("expected %#v to fail", opts)
		}
	}
}

func TestDockerHomeVolumeNameSanitizesLabel(t *testing.T) {
	if got, want := dockerHomeVolumeName("review env"), "toby-home-review-env"; got != want {
		t.Fatalf("volume name = %q, want %q", got, want)
	}
}

func TestDockerHelpersFormatAndSortValues(t *testing.T) {
	if got, want := dockerBind("/host", "/target", false), "type=bind,source=/host,target=/target"; got != want {
		t.Fatalf("dockerBind = %q, want %q", got, want)
	}
	if got, want := dockerBind("/host", "/target", true), "type=bind,source=/host,target=/target,readonly"; got != want {
		t.Fatalf("readonly dockerBind = %q, want %q", got, want)
	}
	if got, want := dockerVolume("home", "/home/demo"), "type=volume,source=home,target=/home/demo"; got != want {
		t.Fatalf("dockerVolume = %q, want %q", got, want)
	}
	if got, want := dockerHomeVolumeName("!!!"), "toby-home-sandbox"; got != want {
		t.Fatalf("fallback volume name = %q, want %q", got, want)
	}
	for _, r := range []rune{'a', 'Z', '0', '_', '.', '-'} {
		if !isDockerVolumeNameChar(r) {
			t.Fatalf("%q should be a docker volume name char", r)
		}
	}
	if isDockerVolumeNameChar(' ') {
		t.Fatal("space should not be a docker volume name char")
	}
	if got, want := dockerEnv(tool.Environment{"B": "2", "A": "1"}), []string{"A=1", "B=2"}; !slices.Equal(got, want) {
		t.Fatalf("dockerEnv = %#v, want %#v", got, want)
	}
}

func testPaths(home string) config.Paths {
	return config.Paths{
		Home:        home,
		ProjectRoot: filepath.Join(home, "Projects"),
		SandboxRoot: filepath.Join(home, "Scratch", "Toby"),
	}
}

func testFactory(paths config.Paths, runner executil.Runner) sandbox.Factory {
	factory, err := sandbox.NewFactory(paths, []sandbox.Environment{
		newDockerEnvironment(paths, runner, "docker", nil),
	})
	if err != nil {
		panic(err)
	}
	return factory
}

func assertContainsSequence(t *testing.T, values, sequence []string) {
	t.Helper()
	for i := 0; i+len(sequence) <= len(values); i++ {
		if slices.Equal(values[i:i+len(sequence)], sequence) {
			return
		}
	}
	t.Fatalf("%#v does not contain sequence %#v", values, sequence)
}

func assertNotContainsSequence(t *testing.T, values, sequence []string) {
	t.Helper()
	for i := 0; i+len(sequence) <= len(values); i++ {
		if slices.Equal(values[i:i+len(sequence)], sequence) {
			t.Fatalf("%#v contains sequence %#v", values, sequence)
		}
	}
}

func assertNoDockerEnv(t *testing.T, values []string, name string) {
	t.Helper()
	prefix := name + "="
	for i := 0; i+1 < len(values); i++ {
		if values[i] == "--env" && strings.HasPrefix(values[i+1], prefix) {
			t.Fatalf("%#v contains docker env %s", values, name)
		}
	}
}
