package sandbox

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"petris.dev/toby/internal/config"
	"petris.dev/toby/internal/control"
	sandboxmount "petris.dev/toby/internal/sandbox/mount"
	sandboxpath "petris.dev/toby/internal/sandbox/path"
	"petris.dev/toby/internal/tools/tool"
)

type testEnvironment struct {
	name     string
	priority int
	paths    config.Paths
}

func (e testEnvironment) Name() string { return e.name }

func (e testEnvironment) Priority() int { return e.priority }

func (e testEnvironment) Available() error { return nil }

func (e testEnvironment) NewInstance(spec Spec) (Instance, error) {
	sandboxPaths := sandboxpath.Defaults()
	base, err := NewBaseInstance(BaseInstanceParams{
		Paths:       e.paths,
		Label:       spec.Label,
		PathSet:     sandboxPaths,
		HomeDir:     sandboxPaths.Home,
		ProjectsDir: sandboxPaths.Workspace,
		RuntimeDir:  sandboxPaths.Root,
		Workdir:     spec.Workdir,
		Projects:    spec.Projects,
	})
	if err != nil {
		return nil, err
	}
	return &testInstance{BaseInstance: base}, nil
}

type testInstance struct {
	BaseInstance
}

func (i *testInstance) Run(context.Context, RunSpec) (int, error) { return 0, nil }

func (i *testInstance) Prime(context.Context, RunSpec) (int, error) { return 0, nil }

func (i *testInstance) Setup(context.Context, RunSpec) (int, error) { return 0, nil }

func TestProjectOutsideHomeRejected(t *testing.T) {
	home := t.TempDir()
	outside := t.TempDir()
	paths := testPaths(home)
	factory := testFactory(paths)
	_, err := factory.FromOptions(&tool.CommandOptions{Env: "demo", Project: outside})
	if err == nil {
		t.Fatal("expected project outside home to be rejected")
	}
}

func TestProjectUnderProjectRootAccepted(t *testing.T) {
	home := t.TempDir()
	projectDir := filepath.Join(home, "Projects", "src", "demo")
	if err := os.MkdirAll(projectDir, 0o755); err != nil {
		t.Fatal(err)
	}
	factory := testFactory(testPaths(home))
	sbx, err := factory.FromOptions(&tool.CommandOptions{Env: "demo", Project: projectDir})
	if err != nil {
		t.Fatal(err)
	}
	visible, ok := sbx.ProjectPath("demo")
	if !ok || visible != filepath.Join(sandboxpath.DefaultWorkspace, "demo") {
		t.Fatalf("project path = %q, %v", visible, ok)
	}
	hostPath, err := sbx.VisibleHostPath("demo")
	if err != nil {
		t.Fatal(err)
	}
	if hostPath != projectDir {
		t.Fatalf("visible host path = %q, want %q", hostPath, projectDir)
	}
}

func TestConfiguredProjectVisibleHostPathUsesProjectName(t *testing.T) {
	home := t.TempDir()
	source := filepath.Join(t.TempDir(), "source")
	nested := filepath.Join(source, "nested")
	if err := os.MkdirAll(nested, 0o755); err != nil {
		t.Fatal(err)
	}
	factory := testFactory(testPaths(home))
	sbx, err := factory.FromOptions(&tool.CommandOptions{
		Env:      "env",
		Projects: []tool.ProjectMount{{Name: "baz", Source: source}},
	})
	if err != nil {
		t.Fatal(err)
	}
	visible, err := sbx.VisibleHostPath("baz/nested")
	if err != nil {
		t.Fatal(err)
	}
	if visible != nested {
		t.Fatalf("visible path = %q, want %q", visible, nested)
	}
	if _, err := sbx.VisibleHostPath("source/nested"); err == nil {
		t.Fatal("expected source path name to be invisible")
	}
}

func TestConfiguredProjectsAllowSameSourceWithDifferentNames(t *testing.T) {
	home := t.TempDir()
	source := filepath.Join(t.TempDir(), "source")
	nested := filepath.Join(source, "nested")
	if err := os.MkdirAll(nested, 0o755); err != nil {
		t.Fatal(err)
	}
	factory := testFactory(testPaths(home))
	sbx, err := factory.FromOptions(&tool.CommandOptions{
		Env:      "env",
		Projects: []tool.ProjectMount{{Name: "foo", Source: source}, {Name: "bar", Source: source}},
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"foo", "bar"} {
		visible, err := sbx.VisibleHostPath(name + "/nested")
		if err != nil {
			t.Fatal(err)
		}
		if visible != nested {
			t.Fatalf("visible path for %s = %q, want %q", name, visible, nested)
		}
	}
}

func TestVisibleHostPathAllowsNestedRepositoryUnderVisibleProject(t *testing.T) {
	home := t.TempDir()
	paths := testPaths(home)
	project := filepath.Join(paths.ProjectRoot, "foobar")
	nested := filepath.Join(project, "baz", "bat")
	if err := os.MkdirAll(nested, 0o755); err != nil {
		t.Fatal(err)
	}
	factory := testFactory(paths)
	sbx, err := factory.FromOptions(&tool.CommandOptions{Env: "foobar"})
	if err != nil {
		t.Fatal(err)
	}
	visible, err := sbx.VisibleHostPath("foobar/baz/bat")
	if err != nil {
		t.Fatal(err)
	}
	if visible != nested {
		t.Fatalf("visible path = %q, want %q", visible, nested)
	}
}

func TestVisibleHostPathRejectsDotSegmentRepository(t *testing.T) {
	home := t.TempDir()
	paths := testPaths(home)
	project := filepath.Join(paths.ProjectRoot, "foobar")
	if err := os.MkdirAll(project, 0o755); err != nil {
		t.Fatal(err)
	}
	factory := testFactory(paths)
	sbx, err := factory.FromOptions(&tool.CommandOptions{Env: "foobar"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := sbx.VisibleHostPath("foobar/../baz"); err == nil {
		t.Fatal("expected dot segment repository to be rejected")
	}
}

func TestVisibleHostPathRejectsInvisibleRepository(t *testing.T) {
	home := t.TempDir()
	paths := testPaths(home)
	project := filepath.Join(paths.ProjectRoot, "foobar")
	if err := os.MkdirAll(project, 0o755); err != nil {
		t.Fatal(err)
	}
	factory := testFactory(paths)
	sbx, err := factory.FromOptions(&tool.CommandOptions{Env: "foobar"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := sbx.VisibleHostPath("other"); err == nil {
		t.Fatal("expected invisible repository to be rejected")
	}
}

func TestVisibleHostPathRejectsSymlinkEscape(t *testing.T) {
	home := t.TempDir()
	paths := testPaths(home)
	project := filepath.Join(paths.ProjectRoot, "foobar")
	outside := filepath.Join(t.TempDir(), "outside")
	if err := os.MkdirAll(project, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(outside, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(project, "link")); err != nil {
		t.Fatal(err)
	}
	factory := testFactory(paths)
	sbx, err := factory.FromOptions(&tool.CommandOptions{Env: "foobar"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := sbx.VisibleHostPath("foobar/link"); err == nil {
		t.Fatal("expected symlink escape to be rejected")
	}
}

func TestFactoryResolvesRelativeMountHostRootFromPrimaryProject(t *testing.T) {
	home := t.TempDir()
	paths := testPaths(home)
	projectDir := filepath.Join(paths.ProjectRoot, "demo")
	if err := os.MkdirAll(projectDir, 0o755); err != nil {
		t.Fatal(err)
	}
	factory := testFactory(paths)
	opts := &tool.CommandOptions{
		Env:            "demo",
		SandboxRuntime: RuntimeBubblewrap,
		MountProfiles: sandboxmount.Profiles{"default": {
			Backing:  sandboxmount.BackingHost,
			HostRoot: "state/root",
		}},
	}
	if _, err := factory.FromOptions(opts); err != nil {
		t.Fatal(err)
	}
	if got, want := opts.MountProfiles.Config("default").HostRootFor(sandboxmount.Key{Type: sandboxmount.TypeTool, Name: tool.OpenCodeToolName, Purpose: "config"}), filepath.Join(projectDir, "state", "root"); got != want {
		t.Fatalf("host root = %q, want %q", got, want)
	}
}

func TestFactoryIgnoresDormantDockerProviderConfig(t *testing.T) {
	home := t.TempDir()
	paths := testPaths(home)
	projectDir := filepath.Join(paths.ProjectRoot, "demo")
	if err := os.MkdirAll(projectDir, 0o755); err != nil {
		t.Fatal(err)
	}
	factory := testFactory(paths)
	_, err := factory.FromOptions(&tool.CommandOptions{
		Env:            "demo",
		SandboxRuntime: RuntimeBubblewrap,
		DockerImage:    "node:test",
		DockerHome:     "/home/docker",
		DockerProjects: "/workspace/docker",
		DockerBuild:    tool.DockerBuildConfig{Context: home, Dockerfile: filepath.Join(home, "Dockerfile")},
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestSandboxAndProjectNamesRejectSlashes(t *testing.T) {
	home := t.TempDir()
	paths := testPaths(home)
	projectDir := filepath.Join(paths.ProjectRoot, "demo")
	if err := os.MkdirAll(projectDir, 0o755); err != nil {
		t.Fatal(err)
	}
	factory := testFactory(paths)
	if _, err := factory.FromOptions(&tool.CommandOptions{Env: "team/demo"}); err == nil {
		t.Fatal("expected slash in sandbox name to be rejected")
	}
	if _, err := factory.FromOptions(&tool.CommandOptions{Projects: []tool.ProjectMount{{Name: "team/demo", Source: projectDir}}}); err == nil {
		t.Fatal("expected slash in project name to be rejected")
	}
}

func TestBaseInstancePathAndEndpointHelpers(t *testing.T) {
	paths := testPaths(t.TempDir())
	instance := &BaseInstance{
		paths:              paths,
		label:              "demo",
		sandboxPaths:       sandboxpath.Defaults(),
		homeDir:            sandboxpath.DefaultHome,
		projectsDir:        sandboxpath.DefaultWorkspace,
		runtimeDir:         sandboxpath.DefaultRoot,
		controlToken:       "host-token",
		sandboxControlHost: "host.docker.internal",
		projects:           newProjectMounts([]Project{{Name: "app", HostPath: "/host/app"}}, sandboxpath.DefaultWorkspace),
	}
	if path, ok := instance.ProjectPath(" app "); !ok || path != filepath.Join(sandboxpath.DefaultWorkspace, "app") {
		t.Fatalf("ProjectPath = %q, %v", path, ok)
	}
	if instance.TobyBinaryPath() != filepath.Join(sandboxpath.DefaultBin, "toby") || instance.TobyGitAgentsPath() != filepath.Join(sandboxpath.DefaultContext, "GIT_AGENTS.md") {
		t.Fatalf("runtime paths: bin=%q git=%q", instance.TobyBinaryPath(), instance.TobyGitAgentsPath())
	}
	if got := instance.sandboxHost("127.0.0.1:1234"); got != "host.docker.internal:1234" {
		t.Fatalf("sandboxHost ipv4 = %q", got)
	}
	if got := instance.sandboxHost("[::1]:1234"); got != "host.docker.internal:1234" {
		t.Fatalf("sandboxHost ipv6 = %q", got)
	}
	if got := instance.sandboxHost("10.0.0.1:1234"); got != "10.0.0.1:1234" {
		t.Fatalf("sandboxHost external = %q", got)
	}
	env := tool.Environment{}
	instance.SetupControlEndpoint(env, control.Endpoint{Host: "127.0.0.1:1234", Token: "token"})
	if env[control.EnvControlHost] != "host.docker.internal:1234" || env[control.EnvControlToken] != "token" {
		t.Fatalf("control env = %#v", env)
	}
	instance.workdir = "~/work"
	if got := instance.ChdirDir(); got != filepath.Join(sandboxpath.DefaultHome, "work") {
		t.Fatalf("chdir workdir = %q", got)
	}
	instance.workdir = ""
	if got := instance.ChdirDir(); got != filepath.Join(sandboxpath.DefaultWorkspace, "app") {
		t.Fatalf("chdir project = %q", got)
	}
}

func TestSandboxNameAndRepositoryValidationHelpers(t *testing.T) {
	if err := validateRelativeName("sandbox", " demo "); err != nil {
		t.Fatal(err)
	}
	for _, value := range []string{"", ".", "..", "team/demo", "/demo", "demo\x00"} {
		if err := validateRelativeName("sandbox", value); err == nil {
			t.Fatalf("expected relative name %q to fail", value)
		}
	}
	if got, err := repositoryName(" foo/bar "); err != nil || got != "foo/bar" {
		t.Fatalf("repositoryName = %q, %v", got, err)
	}
	for _, value := range []string{"", "foo//bar", "foo/../bar", "/foo", "foo\x00"} {
		if _, err := repositoryName(value); err == nil {
			t.Fatalf("expected repository %q to fail", value)
		}
	}
	if !nameWithin("foo", "foo/bar") || nameWithin("foo", "foobar") {
		t.Fatal("unexpected nameWithin result")
	}
	base := t.TempDir()
	child := filepath.Join(base, "child")
	if rel, err := relativeTo(base, child); err != nil || rel != "child" {
		t.Fatalf("relativeTo child = %q, %v", rel, err)
	}
	if _, err := relativeTo(base, filepath.Join(filepath.Dir(base), "outside")); err == nil {
		t.Fatal("expected outside path to fail")
	}
}

func testPaths(home string) config.Paths {
	return config.Paths{
		Home:        home,
		ProjectRoot: filepath.Join(home, "Projects"),
		SandboxRoot: filepath.Join(home, "Scratch", "Toby"),
	}
}

func testFactory(paths config.Paths) Factory {
	factory, err := NewFactory(paths, []Environment{
		testEnvironment{name: RuntimeDocker, paths: paths},
		testEnvironment{name: RuntimeBubblewrap, priority: 1, paths: paths},
	})
	if err != nil {
		panic(err)
	}
	return factory
}
