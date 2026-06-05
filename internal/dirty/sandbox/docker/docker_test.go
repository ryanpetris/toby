package docker

import (
	"path/filepath"
	"slices"
	"testing"

	"petris.dev/toby/config"
	"petris.dev/toby/container/engine"
	"petris.dev/toby/container/layout"
	"petris.dev/toby/container/mount"
	"petris.dev/toby/internal/dirty/sandbox"
	"petris.dev/toby/platform/environ"

	dcontainer "github.com/moby/moby/api/types/container"
	dmount "github.com/moby/moby/api/types/mount"
	"github.com/testcontainers/testcontainers-go"
)

func testPaths(home string) config.Paths {
	return config.Paths{
		Home:        home,
		ProjectRoot: filepath.Join(home, "Projects"),
		SandboxRoot: filepath.Join(home, "Scratch", "Toby"),
	}
}

// dockerInstance builds an *instance directly via NewInstance (which needs no
// Docker daemon) so the pure containerRequest builder can be unit-tested.
func dockerInstance(t *testing.T, mutate func(*sandbox.Spec)) (*instance, string) {
	t.Helper()
	home := t.TempDir()
	paths := testPaths(home)
	projectDir := filepath.Join(paths.ProjectRoot, "demo")
	spec := sandbox.Spec{Label: "demo", Projects: []sandbox.Project{{Name: "demo", HostPath: projectDir}}}
	if mutate != nil {
		mutate(&spec)
	}
	inst, err := newRuntime(paths, engine.New()).NewInstance(spec)
	if err != nil {
		t.Fatalf("NewInstance: %v", err)
	}
	return inst.(*instance), projectDir
}

func dockerHomeMount(target string) []mount.Entry {
	homeKey := mount.RuntimeHomeKey("demo")
	return []mount.Entry{{
		Key:    homeKey,
		Volume: mount.Volume("default", homeKey),
		Target: target,
	}}
}

func applyConfig(req testcontainers.GenericContainerRequest) *dcontainer.Config {
	c := &dcontainer.Config{}
	if req.ConfigModifier != nil {
		req.ConfigModifier(c)
	}
	return c
}

func applyHostConfig(req testcontainers.GenericContainerRequest) *dcontainer.HostConfig {
	h := &dcontainer.HostConfig{}
	if req.HostConfigModifier != nil {
		req.HostConfigModifier(h)
	}
	return h
}

func findMount(mounts []dmount.Mount, target string) (dmount.Mount, bool) {
	for _, m := range mounts {
		if m.Target == target {
			return m, true
		}
	}
	return dmount.Mount{}, false
}

func TestContainerRequestRunMountsEnvAndHostNetwork(t *testing.T) {
	inst, projectDir := dockerInstance(t, nil)
	home := inst.HomeDir()
	env := environ.Environment{"TOBY_CONTROL_HOST": "127.0.0.1:1234", "TOBY_CONTROL_TOKEN": "secret", "HOME": home}
	mounts := dockerHomeMount(home)
	bindTarget := filepath.Join(home, ".local", "share", "opencode")
	binds := []mount.Bind{{HostPath: "/host/opencode", Target: bindTarget, Access: mount.AccessRegular}}
	argv := []string{inst.TobyBinaryPath(), "sandbox", "manager"}

	req := inst.containerRequest(sandbox.RunSpec{Argv: argv, Env: env, Binds: binds, Mounts: mounts}, phaseRun, engine.DaemonLocalUnix)

	if req.Image != DefaultImage {
		t.Fatalf("image = %q, want %q", req.Image, DefaultImage)
	}
	if !slices.Equal(req.Cmd, argv) {
		t.Fatalf("cmd = %#v", req.Cmd)
	}
	if req.Env["TOBY_CONTROL_HOST"] != "127.0.0.1:1234" {
		t.Fatalf("control host = %q (local should be unchanged)", req.Env["TOBY_CONTROL_HOST"])
	}
	if req.Env["TOBY_CONTROL_TOKEN"] != "secret" || req.Env["HOME"] != home {
		t.Fatalf("env = %#v", req.Env)
	}
	if req.HostAccessPorts != nil {
		t.Fatalf("local daemon should not use host-access tunnel: %#v", req.HostAccessPorts)
	}

	cfg := applyConfig(req)
	if cfg.User != "0:0" {
		t.Fatalf("user = %q", cfg.User)
	}
	if !cfg.OpenStdin || !cfg.AttachStdin {
		t.Fatalf("run config should keep stdin open: %#v", cfg)
	}

	hc := applyHostConfig(req)
	if hc.NetworkMode != "host" {
		t.Fatalf("network mode = %q, want host", hc.NetworkMode)
	}
	if v, ok := findMount(hc.Mounts, home); !ok || v.Type != dmount.TypeVolume || v.Source != mounts[0].Volume {
		t.Fatalf("home volume mount = %#v ok=%v", v, ok)
	}
	projectTarget := inst.ProjectMounts()[0].SandboxPath
	if b, ok := findMount(hc.Mounts, projectTarget); !ok || b.Type != dmount.TypeBind || b.Source != projectDir {
		t.Fatalf("project bind = %#v ok=%v", b, ok)
	}
	if b, ok := findMount(hc.Mounts, bindTarget); !ok || b.Source != "/host/opencode" {
		t.Fatalf("explicit bind = %#v ok=%v", b, ok)
	}
}

func TestContainerRequestPrimeEntrypointAndMounts(t *testing.T) {
	inst, _ := dockerInstance(t, nil)
	home := inst.HomeDir()
	req := inst.containerRequest(sandbox.RunSpec{Mounts: dockerHomeMount(home)}, phasePrime, engine.DaemonLocalUnix)

	if !slices.Equal(req.Cmd, []string{"-c", "exit"}) {
		t.Fatalf("cmd = %#v", req.Cmd)
	}
	cfg := applyConfig(req)
	if !slices.Equal(cfg.Entrypoint, []string{"/bin/sh"}) {
		t.Fatalf("entrypoint = %#v", cfg.Entrypoint)
	}
	hc := applyHostConfig(req)
	if hc.Init != nil {
		t.Fatal("prime should not request an init process")
	}
	if hc.NetworkMode != "" {
		t.Fatalf("prime should not set a network mode: %q", hc.NetworkMode)
	}
	if _, ok := findMount(hc.Mounts, home); !ok {
		t.Fatal("prime must seed the home volume at its final target")
	}
}

func TestContainerRequestSetupUsesSetupPathAndInit(t *testing.T) {
	inst, _ := dockerInstance(t, nil)
	home := inst.HomeDir()
	m := mount.Entry{
		Volume:    "toby.demo.tool.opencode.config",
		Target:    filepath.Join(home, ".config", "opencode"),
		SetupPath: filepath.Join(layout.Root, "mounts", "opencode-config"),
	}
	argv := []string{inst.TobyBinaryPath(), "sandbox", "manager"}
	req := inst.containerRequest(sandbox.RunSpec{Argv: argv, Env: environ.Environment{"HOME": home}, Mounts: []mount.Entry{m}}, phaseSetup, engine.DaemonLocalUnix)

	if !slices.Equal(req.Cmd, argv) {
		t.Fatalf("cmd = %#v", req.Cmd)
	}
	if cfg := applyConfig(req); cfg.WorkingDir != "/" {
		t.Fatalf("setup workdir = %q", cfg.WorkingDir)
	}
	hc := applyHostConfig(req)
	if hc.Init == nil || !*hc.Init {
		t.Fatal("setup should request an init process")
	}
	if v, ok := findMount(hc.Mounts, m.SetupPath); !ok || v.Source != m.Volume {
		t.Fatalf("setup should mount the provider volume at its setup path: %#v ok=%v", v, ok)
	}
	if _, ok := findMount(hc.Mounts, m.Target); ok {
		t.Fatal("setup must not mount the final target")
	}
}

func TestRewriteControlHost(t *testing.T) {
	cases := []struct {
		class engine.DaemonClass
		want  string
	}{
		{engine.DaemonLocalUnix, "127.0.0.1:1234"},
		{engine.DaemonDesktop, "host.docker.internal:1234"},
		{engine.DaemonRemote, "host.testcontainers.internal:1234"},
	}
	for _, c := range cases {
		if got := rewriteControlHost("127.0.0.1:1234", c.class); got != c.want {
			t.Fatalf("class %d => %q, want %q", c.class, got, c.want)
		}
	}
}

func TestContainerRequestRemoteUsesHostAccessTunnel(t *testing.T) {
	inst, _ := dockerInstance(t, nil)
	env := environ.Environment{"TOBY_CONTROL_HOST": "127.0.0.1:1234", "HOME": inst.HomeDir()}
	req := inst.containerRequest(sandbox.RunSpec{Argv: []string{"true"}, Env: env, Mounts: dockerHomeMount(inst.HomeDir())}, phaseRun, engine.DaemonRemote)

	if !slices.Equal(req.HostAccessPorts, []int{1234}) {
		t.Fatalf("host access ports = %#v", req.HostAccessPorts)
	}
	if req.Env["TOBY_CONTROL_HOST"] != "host.testcontainers.internal:1234" {
		t.Fatalf("control host = %q", req.Env["TOBY_CONTROL_HOST"])
	}
	if hc := applyHostConfig(req); hc.NetworkMode == "host" {
		t.Fatal("remote daemon must not use host networking")
	}
}

func TestContainerRequestDesktopUsesHostDockerInternal(t *testing.T) {
	inst, _ := dockerInstance(t, nil)
	env := environ.Environment{"TOBY_CONTROL_HOST": "127.0.0.1:1234", "HOME": inst.HomeDir()}
	req := inst.containerRequest(sandbox.RunSpec{Argv: []string{"true"}, Env: env, Mounts: dockerHomeMount(inst.HomeDir())}, phaseRun, engine.DaemonDesktop)

	if req.Env["TOBY_CONTROL_HOST"] != "host.docker.internal:1234" {
		t.Fatalf("control host = %q", req.Env["TOBY_CONTROL_HOST"])
	}
	if req.HostAccessPorts != nil {
		t.Fatalf("desktop should not use host-access tunnel: %#v", req.HostAccessPorts)
	}
	if hc := applyHostConfig(req); hc.NetworkMode == "host" {
		t.Fatal("desktop must not use host networking")
	}
}

func TestContainerRequestDebugNamesContainers(t *testing.T) {
	inst, _ := dockerInstance(t, nil)
	spec := sandbox.RunSpec{Argv: []string{"true"}, Env: environ.Environment{}, Mounts: dockerHomeMount(inst.HomeDir()), Debug: true}
	for phase, suffix := range map[phaseKind]string{phasePrime: "-prime", phaseSetup: "-setup", phaseRun: "-run"} {
		if got := inst.containerRequest(spec, phase, engine.DaemonLocalUnix).Name; got != inst.containerName+suffix {
			t.Fatalf("%s name = %q, want %q", phase, got, inst.containerName+suffix)
		}
	}
	if got := inst.containerRequest(sandbox.RunSpec{Argv: []string{"true"}, Env: environ.Environment{}}, phaseRun, engine.DaemonLocalUnix).Name; got != "" {
		t.Fatalf("non-debug name = %q, want empty", got)
	}
}

func TestNewInstanceAppliesImage(t *testing.T) {
	inst, _ := dockerInstance(t, func(s *sandbox.Spec) {
		s.Image = "custom:latest"
	})
	if inst.HomeDir() != layout.Home {
		t.Fatalf("HomeDir = %q", inst.HomeDir())
	}
	if inst.Projects() != layout.Workspace {
		t.Fatalf("Projects = %q", inst.Projects())
	}
	if inst.image != "custom:latest" {
		t.Fatalf("image = %q", inst.image)
	}
}

func TestMountHelpers(t *testing.T) {
	b := bindMount("/host", "/target", true)
	if b.Type != dmount.TypeBind || b.Source != "/host" || b.Target != "/target" || !b.ReadOnly {
		t.Fatalf("bindMount = %#v", b)
	}
	v := volumeMount("vol", "/target")
	if v.Type != dmount.TypeVolume || v.Source != "vol" || v.Target != "/target" || v.ReadOnly {
		t.Fatalf("volumeMount = %#v", v)
	}
}

func TestControlEnvIncludesTermAndControlVars(t *testing.T) {
	t.Setenv("TERM", "xterm-256color")
	env := controlEnv(environ.Environment{"HOME": "/h", "TOBY_CONTROL_HOST": "127.0.0.1:9", "TOBY_CONTROL_TOKEN": "tok"}, engine.DaemonLocalUnix)
	if env["TERM"] != "xterm-256color" {
		t.Fatalf("TERM = %q", env["TERM"])
	}
	if env["HOME"] != "/h" || env["TOBY_CONTROL_TOKEN"] != "tok" {
		t.Fatalf("controlEnv = %#v", env)
	}
}
