package sandbox

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"petris.dev/toby/fusekit"
	"petris.dev/toby/internal/config"
	"petris.dev/toby/internal/executil"
	"petris.dev/toby/internal/tool"
)

type fakeRunner struct{}

func (fakeRunner) Run(context.Context, []string, map[string]string, executil.Options) (int, error) {
	return 0, nil
}

type bindTool struct {
	tool.Base
	binds []tool.Bind
}

func (t bindTool) Binds() []tool.Bind {
	return append([]tool.Bind(nil), t.binds...)
}

func TestBuildCommandUsesFuseHomeAndSelectedBwrapBindings(t *testing.T) {
	home := t.TempDir()
	projectRoot := filepath.Join(home, "Projects")
	projectDir := filepath.Join(projectRoot, "demo")
	if err := os.MkdirAll(projectDir, 0o755); err != nil {
		t.Fatal(err)
	}
	paths := config.Paths{
		Home:           home,
		ProjectRoot:    projectRoot,
		SandboxRoot:    filepath.Join(home, "Scratch", "Toby"),
		StateHome:      filepath.Join(home, ".local", "state"),
		XDGRuntimeDir:  "/run/user/1234",
		PipewireCore:   "pipewire-test",
		WaylandDisplay: "wayland-test",
		XAuthority:     filepath.Join(home, ".Xauthority"),
	}
	factory := NewFactory(paths, fakeRunner{})
	sbx, err := factory.FromOptions(&tool.CommandOptions{Env: "demo", MountableProjects: true})
	if err != nil {
		t.Fatal(err)
	}
	regularSandboxPath := filepath.Join(home, ".config", "regular")
	readonlySandboxPath := filepath.Join(home, ".config", "readonly")
	devSandboxPath := "/var/run/demo.sock"
	registry, err := tool.NewRegistry(tool.RegistryParams{Tools: []tool.Tool{bindTool{
		Base: tool.Base{Metadata: tool.Metadata{Name: "bind"}},
		binds: []tool.Bind{
			{HostPath: "/host/regular", SandboxPath: regularSandboxPath, Type: tool.BindRegular},
			{HostPath: "/host/readonly", SandboxPath: readonlySandboxPath, Type: tool.BindReadOnly, Optional: true},
			{HostPath: "/host/demo.sock", SandboxPath: devSandboxPath, Type: tool.BindDev, Optional: true},
		},
	}}})
	if err != nil {
		t.Fatal(err)
	}
	toolset, err := registry.Build([]string{"bind"}, "")
	if err != nil {
		t.Fatal(err)
	}
	homeMounts, bwrapBinds, visible, err := sbx.buildHomeMounts(toolset)
	if err != nil {
		t.Fatal(err)
	}
	assertHomeMounts(t, homeMounts, []mountExpectation{
		{id: "runtime-root", base: "/"},
		{id: "projects-root", base: "/projects"},
		{id: "project", base: "/projects/demo", source: projectDir, readOnly: false},
	})
	if len(bwrapBinds) != 3 {
		t.Fatalf("bwrap binds = %#v", bwrapBinds)
	}
	if len(visible) != 1 || visible[0].Base != "/projects/demo" || visible[0].Source != projectDir {
		t.Fatalf("visible = %#v", visible)
	}
	fuseMountpoint := filepath.Join(t.TempDir(), "home-fuse")
	cmd := sbx.BuildCommand([]string{"/bin/true"}, CommandMounts{RuntimeMountpoint: fuseMountpoint, Binds: bwrapBinds})
	assertContainsSequence(t, cmd, []string{"/usr/bin/bwrap", "--die-with-parent", "--unshare-pid"})
	assertContainsSequence(t, cmd, []string{"--dev-bind", "/dev", "/dev"})
	assertContainsSequence(t, cmd, []string{"--bind-try", "/sys", "/sys"})
	assertContainsSequence(t, cmd, []string{"--tmpfs", "/run/user/1234"})
	assertContainsSequence(t, cmd, []string{"--ro-bind-try", "/run/user/1234/pulse", "/run/user/1234/pulse"})
	assertContainsSequence(t, cmd, []string{"--ro-bind-try", "/run/user/1234/pipewire-test", "/run/user/1234/pipewire-test"})
	assertContainsSequence(t, cmd, []string{"--ro-bind-try", "/run/user/1234/wayland-test", "/run/user/1234/wayland-test"})
	assertContainsSequence(t, cmd, []string{"--ro-bind-try", "/run/udev", "/run/udev"})
	assertContainsSequence(t, cmd, []string{"--ro-bind-try", paths.XAuthority, paths.XAuthority})
	assertContainsSequence(t, cmd, []string{"--bind", sbx.HomeDir(), home})
	assertContainsSequence(t, cmd, []string{"--bind", filepath.Join(fuseMountpoint, "projects"), projectRoot})
	assertContainsSequence(t, cmd, []string{"--bind", filepath.Join(fuseMountpoint, "toby"), filepath.Join(home, ".local", "state", "toby")})
	assertContainsSequence(t, cmd, []string{"--bind", "/usr/bin/true", "/usr/bin/xdg-open"})
	assertContainsSequence(t, cmd, []string{"--bind", "/host/regular", regularSandboxPath})
	assertContainsSequence(t, cmd, []string{"--ro-bind-try", "/host/readonly", readonlySandboxPath})
	assertContainsSequence(t, cmd, []string{"--dev-bind-try", "/host/demo.sock", devSandboxPath})
	assertNotContainsSequence(t, cmd, []string{"--bind", fuseMountpoint, home})
	assertNotContainsSequence(t, cmd, []string{"--bind-try", projectDir, projectDir})
	assertContainsSequence(t, cmd, []string{"--chdir", projectDir})
	if slices.Contains(cmd, "/run/dbus") || slices.Contains(cmd, "/run/user/1234/bus") {
		t.Fatalf("command unexpectedly includes dbus bindings: %#v", cmd)
	}
}

func TestBuildCommandBindsSingleProjectWithoutMountableProjects(t *testing.T) {
	home := t.TempDir()
	projectRoot := filepath.Join(home, "Projects")
	projectDir := filepath.Join(projectRoot, "demo")
	if err := os.MkdirAll(projectDir, 0o755); err != nil {
		t.Fatal(err)
	}
	paths := testPaths(home)
	paths.XAuthority = filepath.Join(home, ".Xauthority")
	factory := NewFactory(paths, fakeRunner{})
	sbx, err := factory.FromOptions(&tool.CommandOptions{Env: "demo"})
	if err != nil {
		t.Fatal(err)
	}
	registry, err := tool.NewRegistry(tool.RegistryParams{})
	if err != nil {
		t.Fatal(err)
	}
	toolset, err := registry.Build(nil, "")
	if err != nil {
		t.Fatal(err)
	}
	homeMounts, _, visible, err := sbx.buildHomeMounts(toolset)
	if err != nil {
		t.Fatal(err)
	}
	assertHomeMounts(t, homeMounts, []mountExpectation{
		{id: "runtime-root", base: "/"},
	})
	if len(visible) != 1 || visible[0].Base != "/projects/demo" || visible[0].Source != projectDir {
		t.Fatalf("visible = %#v", visible)
	}
	cmd := sbx.BuildCommand([]string{"/bin/true"}, CommandMounts{})
	assertContainsSequence(t, cmd, []string{"--bind", projectDir, projectDir})
	assertNotContainsSequence(t, cmd, []string{"--bind", filepath.Join("", "projects"), projectRoot})
	assertNotContainsSequence(t, cmd, []string{"--bind", "toby", filepath.Join(home, ".local", "state", "toby")})
}

func TestProjectOutsideHomeRejected(t *testing.T) {
	home := t.TempDir()
	outside := t.TempDir()
	paths := testPaths(home)
	factory := NewFactory(paths, fakeRunner{})
	_, err := factory.FromOptions(&tool.CommandOptions{Env: "demo", Project: outside})
	if err == nil {
		t.Fatal("expected project outside home to be rejected")
	}
}

func TestProjectSymlinkPathIsPreserved(t *testing.T) {
	home := t.TempDir()
	actualRoot := filepath.Join(home, "Scratch", "Projects")
	actualProject := filepath.Join(actualRoot, "demo")
	if err := os.MkdirAll(actualProject, 0o755); err != nil {
		t.Fatal(err)
	}
	linkRoot := filepath.Join(home, "Projects")
	if err := os.Symlink(actualRoot, linkRoot); err != nil {
		t.Fatal(err)
	}
	paths := testPaths(home)
	paths.ProjectRoot = linkRoot
	factory := NewFactory(paths, fakeRunner{})
	sbx, err := factory.FromOptions(&tool.CommandOptions{Env: "demo", MountableProjects: true})
	if err != nil {
		t.Fatal(err)
	}
	lexicalProject := filepath.Join(linkRoot, "demo")
	if sbx.projectDir != lexicalProject {
		t.Fatalf("projectDir = %q, want lexical path %q", sbx.projectDir, lexicalProject)
	}
	registry, err := tool.NewRegistry(tool.RegistryParams{})
	if err != nil {
		t.Fatal(err)
	}
	toolset, err := registry.Build(nil, "")
	if err != nil {
		t.Fatal(err)
	}
	mounts, _, _, err := sbx.buildHomeMounts(toolset)
	if err != nil {
		t.Fatal(err)
	}
	projectMount := mounts[len(mounts)-1].(*fusekit.PassthroughMount)
	if projectMount.BasePath() != "/projects/demo" {
		t.Fatalf("project mount base = %q, want /projects/demo", projectMount.BasePath())
	}
	if projectMount.Source() != lexicalProject {
		t.Fatalf("project mount source = %q, want lexical path %q", projectMount.Source(), lexicalProject)
	}
}

func TestProjectUnderProjectRootAccepted(t *testing.T) {
	home := t.TempDir()
	projectDir := filepath.Join(home, "Projects", "src", "demo")
	if err := os.MkdirAll(projectDir, 0o755); err != nil {
		t.Fatal(err)
	}
	factory := NewFactory(testPaths(home), fakeRunner{})
	sbx, err := factory.FromOptions(&tool.CommandOptions{Env: "demo", Project: projectDir})
	if err != nil {
		t.Fatal(err)
	}
	if sbx.projectDir != projectDir {
		t.Fatalf("projectDir = %q, want %q", sbx.projectDir, projectDir)
	}
}

func TestOpenCodeConfigDirUsesSandboxRoot(t *testing.T) {
	home := t.TempDir()
	paths := testPaths(home)
	if err := os.MkdirAll(filepath.Join(paths.ProjectRoot, "demo"), 0o755); err != nil {
		t.Fatal(err)
	}
	factory := NewFactory(paths, fakeRunner{})
	sbx, err := factory.FromOptions(&tool.CommandOptions{Env: "demo"})
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(paths.SandboxRoot, ".config", "opencode")
	if sbx.OpenCodeConfigDir() != want {
		t.Fatalf("OpenCodeConfigDir = %q, want %q", sbx.OpenCodeConfigDir(), want)
	}
}

func TestToolBindOutsideHomeUsesBwrap(t *testing.T) {
	home := t.TempDir()
	projectDir := filepath.Join(home, "Projects", "demo")
	if err := os.MkdirAll(projectDir, 0o755); err != nil {
		t.Fatal(err)
	}
	factory := NewFactory(testPaths(home), fakeRunner{})
	sbx, err := factory.FromOptions(&tool.CommandOptions{Env: "demo"})
	if err != nil {
		t.Fatal(err)
	}
	registry, err := tool.NewRegistry(tool.RegistryParams{Tools: []tool.Tool{bindTool{
		Base:  tool.Base{Metadata: tool.Metadata{Name: "bad"}},
		binds: []tool.Bind{{HostPath: "/host", SandboxPath: "/opt/bad", Type: tool.BindReadOnly}},
	}}})
	if err != nil {
		t.Fatal(err)
	}
	toolset, err := registry.Build([]string{"bad"}, "")
	if err != nil {
		t.Fatal(err)
	}
	_, bwrapBinds, _, err := sbx.buildHomeMounts(toolset)
	if err != nil {
		t.Fatal(err)
	}
	if len(bwrapBinds) != 1 || bwrapBinds[0].SandboxPath != "/opt/bad" {
		t.Fatalf("bwrap binds = %#v", bwrapBinds)
	}
}

func TestHomeFSAddHostPathPublishesDynamicMount(t *testing.T) {
	ctx := context.Background()
	projectRoot := t.TempDir()
	hostPath := filepath.Join(projectRoot, "shared")
	if err := os.MkdirAll(hostPath, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(hostPath, "file"), []byte("hello"), 0o644); err != nil {
		t.Fatal(err)
	}
	root, err := fusekit.NewEmptyDirMount("runtime-root", "/", 0o500)
	if err != nil {
		t.Fatal(err)
	}
	router, err := fusekit.NewRouter([]fusekit.Mount{root})
	if err != nil {
		t.Fatal(err)
	}
	homeFS := &HomeFS{router: router, baseMounts: []fusekit.Mount{root}, projectRoot: projectRoot}
	virtual, err := homeFS.AddHostPath(hostPath)
	if err != nil {
		t.Fatal(err)
	}
	if virtual != "/projects/shared" {
		t.Fatalf("virtual path = %q, want /projects/shared", virtual)
	}
	res, err := router.Dispatch(ctx, fusekit.Operation{Kind: fusekit.OpGetAttr, Path: "/projects/shared/file"})
	if err != nil {
		t.Fatal(err)
	}
	if res.Attr.Object.MountID != "dynamic-0" {
		t.Fatalf("mount ID = %q, want dynamic-0", res.Attr.Object.MountID)
	}
}

func TestHomeFSAddHostPathRejectsPathOutsideProjectRoot(t *testing.T) {
	projectRoot := t.TempDir()
	outside := t.TempDir()
	if err := os.MkdirAll(outside, 0o755); err != nil {
		t.Fatal(err)
	}
	root, err := fusekit.NewEmptyDirMount("runtime-root", "/", 0o500)
	if err != nil {
		t.Fatal(err)
	}
	router, err := fusekit.NewRouter([]fusekit.Mount{root})
	if err != nil {
		t.Fatal(err)
	}
	homeFS := &HomeFS{router: router, baseMounts: []fusekit.Mount{root}, projectRoot: projectRoot}
	if _, err := homeFS.AddHostPath(outside); err == nil {
		t.Fatal("expected outside project mount to be rejected")
	}
}

func TestHomeFSVisibleHostPathAllowsNestedRepositoryUnderVisibleProject(t *testing.T) {
	projectRoot := t.TempDir()
	project := filepath.Join(projectRoot, "foobar")
	nested := filepath.Join(project, "baz", "bat")
	if err := os.MkdirAll(nested, 0o755); err != nil {
		t.Fatal(err)
	}
	root, err := fusekit.NewEmptyDirMount("runtime-root", "/", 0o500)
	if err != nil {
		t.Fatal(err)
	}
	projectMount, err := fusekit.NewPassthroughMount(fusekit.PassthroughOptions{ID: "project", BasePath: "/projects/foobar", Source: project})
	if err != nil {
		t.Fatal(err)
	}
	router, err := fusekit.NewRouter([]fusekit.Mount{root, projectMount})
	if err != nil {
		t.Fatal(err)
	}
	homeFS := &HomeFS{router: router, baseMounts: []fusekit.Mount{root, projectMount}, projectRoot: projectRoot}
	visible, err := homeFS.VisibleHostPath("foobar/baz/bat")
	if err != nil {
		t.Fatal(err)
	}
	if visible != nested {
		t.Fatalf("visible path = %q, want %q", visible, nested)
	}
}

func TestHomeFSVisibleHostPathRejectsDotSegmentRepository(t *testing.T) {
	root, err := fusekit.NewEmptyDirMount("runtime-root", "/", 0o500)
	if err != nil {
		t.Fatal(err)
	}
	router, err := fusekit.NewRouter([]fusekit.Mount{root})
	if err != nil {
		t.Fatal(err)
	}
	homeFS := &HomeFS{router: router, baseMounts: []fusekit.Mount{root}, projectRoot: t.TempDir()}
	if _, err := homeFS.VisibleHostPath("foobar/../baz"); err == nil {
		t.Fatal("expected dot segment repository to be rejected")
	}
}

func TestHomeFSVisibleHostPathRejectsInvisibleRepository(t *testing.T) {
	root, err := fusekit.NewEmptyDirMount("runtime-root", "/", 0o500)
	if err != nil {
		t.Fatal(err)
	}
	router, err := fusekit.NewRouter([]fusekit.Mount{root})
	if err != nil {
		t.Fatal(err)
	}
	homeFS := &HomeFS{router: router, baseMounts: []fusekit.Mount{root}, projectRoot: t.TempDir()}
	if _, err := homeFS.VisibleHostPath("foobar"); err == nil {
		t.Fatal("expected invisible repository to be rejected")
	}
}

func TestHomeFSVisibleHostPathRejectsSymlinkEscape(t *testing.T) {
	projectRoot := t.TempDir()
	project := filepath.Join(projectRoot, "foobar")
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
	root, err := fusekit.NewEmptyDirMount("runtime-root", "/", 0o500)
	if err != nil {
		t.Fatal(err)
	}
	projectMount, err := fusekit.NewPassthroughMount(fusekit.PassthroughOptions{ID: "project", BasePath: "/projects/foobar", Source: project})
	if err != nil {
		t.Fatal(err)
	}
	router, err := fusekit.NewRouter([]fusekit.Mount{root, projectMount})
	if err != nil {
		t.Fatal(err)
	}
	homeFS := &HomeFS{router: router, baseMounts: []fusekit.Mount{root, projectMount}, projectRoot: projectRoot}
	if _, err := homeFS.VisibleHostPath("foobar/link"); err == nil {
		t.Fatal("expected symlink escape to be rejected")
	}
}

func TestSetupContextPrependsTobyBin(t *testing.T) {
	home := t.TempDir()
	sbx := &Sandbox{paths: testPaths(home), label: "demo"}
	run := &tool.RunContext{Toolset: &tool.Toolset{}, Env: tool.Environment{"PATH": "/usr/bin"}}
	sbx.SetupContext(run)
	pathEntries := strings.Split(run.Env["PATH"], ":")
	want := []string{filepath.Join(home, ".local", "state", "toby", "bin"), filepath.Join(home, ".local", "bin"), "/usr/bin"}
	if !slices.Equal(pathEntries, want) {
		t.Fatalf("PATH entries = %#v, want %#v", pathEntries, want)
	}
	if run.Env["XDG_STATE_HOME"] != filepath.Join(home, ".local", "state") {
		t.Fatalf("XDG_STATE_HOME = %q", run.Env["XDG_STATE_HOME"])
	}
}

func TestTobyStatePathsUseConfiguredStateHome(t *testing.T) {
	home := t.TempDir()
	paths := testPaths(home)
	paths.StateHome = filepath.Join(home, ".state")
	sbx := &Sandbox{paths: paths, label: "demo"}

	base, err := sbx.TobyMountBasePath()
	if err != nil {
		t.Fatal(err)
	}
	if base != "/toby" {
		t.Fatalf("TobyMountBasePath = %q, want /toby", base)
	}
	if sbx.TobyStaticDir() != filepath.Join(home, ".state", "toby", "static") {
		t.Fatalf("TobyStaticDir = %q", sbx.TobyStaticDir())
	}
	if sbx.TobyGitAgentsPath() != filepath.Join(home, ".state", "toby", "static", "GIT_AGENTS.md") {
		t.Fatalf("TobyGitAgentsPath = %q", sbx.TobyGitAgentsPath())
	}
	if sbx.TobyProjectMountAgentsPath() != filepath.Join(home, ".state", "toby", "static", "PROJECT_MOUNT_AGENTS.md") {
		t.Fatalf("TobyProjectMountAgentsPath = %q", sbx.TobyProjectMountAgentsPath())
	}

	run := &tool.RunContext{Toolset: &tool.Toolset{}, Env: tool.Environment{"PATH": "/usr/bin"}}
	sbx.SetupContext(run)
	pathEntries := strings.Split(run.Env["PATH"], ":")
	if pathEntries[0] != filepath.Join(home, ".state", "toby", "bin") {
		t.Fatalf("PATH entries = %#v, want custom Toby bin first", pathEntries)
	}
	if run.Env["XDG_STATE_HOME"] != filepath.Join(home, ".state") {
		t.Fatalf("XDG_STATE_HOME = %q", run.Env["XDG_STATE_HOME"])
	}
}

func testPaths(home string) config.Paths {
	return config.Paths{
		Home:           home,
		ProjectRoot:    filepath.Join(home, "Projects"),
		SandboxRoot:    filepath.Join(home, "Scratch", "Toby"),
		StateHome:      filepath.Join(home, ".local", "state"),
		XDGRuntimeDir:  "/run/user/1234",
		PipewireCore:   "pipewire-test",
		WaylandDisplay: "wayland-test",
	}
}

type mountExpectation struct {
	id       string
	base     string
	source   string
	readOnly bool
}

func assertHomeMounts(t *testing.T, mounts []fusekit.Mount, want []mountExpectation) {
	t.Helper()
	if len(mounts) != len(want) {
		t.Fatalf("mount count = %d, want %d", len(mounts), len(want))
	}
	for i, expectation := range want {
		if mounts[i].ID() != expectation.id || mounts[i].BasePath() != expectation.base {
			t.Fatalf("mount %d = id=%q base=%q, want %#v", i, mounts[i].ID(), mounts[i].BasePath(), expectation)
		}
		if expectation.source == "" {
			continue
		}
		mount, ok := mounts[i].(*fusekit.PassthroughMount)
		if !ok {
			t.Fatalf("mount %d type = %T, want passthrough", i, mounts[i])
		}
		if mount.Source() != expectation.source || mount.ReadOnly() != expectation.readOnly {
			t.Fatalf("mount %d = source=%q ro=%v, want %#v", i, mount.Source(), mount.ReadOnly(), expectation)
		}
	}
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
			t.Fatalf("%#v unexpectedly contains sequence %#v", values, sequence)
		}
	}
}
