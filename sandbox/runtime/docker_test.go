package runtime

import (
	"path/filepath"
	"slices"
	"testing"

	"petris.dev/toby/container/engine"
	"petris.dev/toby/container/layout"
	"petris.dev/toby/container/mount"
	"petris.dev/toby/platform/environ"

	dcontainer "github.com/moby/moby/api/types/container"
	dmount "github.com/moby/moby/api/types/mount"
	"github.com/testcontainers/testcontainers-go"
)

// dockerInstance builds an *instance directly via the Factory (which needs no
// Docker daemon) so the pure containerRequest builder can be unit-tested.
func dockerInstance(t *testing.T, mutate func(*Spec)) (*instance, string) {
	t.Helper()
	home := t.TempDir()
	paths := testPaths(home)
	projectDir := filepath.Join(paths.ProjectRoot, "demo")
	spec := Spec{Label: "demo", Projects: []Project{{Name: "demo", HostPath: projectDir}}}
	if mutate != nil {
		mutate(&spec)
	}
	inst, err := NewFactory(paths, engine.New()).newInstance(spec)
	if err != nil {
		t.Fatalf("newInstance: %v", err)
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

func TestContainerRequestRunsProxyManager(t *testing.T) {
	inst, projectDir := dockerInstance(t, nil)
	home := inst.HomeDir()
	env := environ.Environment{"HOME": home}
	mounts := dockerHomeMount(home)
	bindTarget := filepath.Join(home, ".local", "share", "opencode")
	binds := []mount.Bind{{HostPath: "/host/opencode", Target: bindTarget, Access: mount.AccessRegular}}

	req := inst.containerRequest(RunSpec{Env: env, Binds: binds, Mounts: mounts})

	if req.Image != DefaultImage {
		t.Fatalf("image = %q, want %q", req.Image, DefaultImage)
	}
	if req.Started {
		t.Fatal("container must be created but not started (binary is copied in first)")
	}
	if !slices.Equal(req.Cmd, []string{"sandbox", "manager"}) {
		t.Fatalf("cmd = %#v", req.Cmd)
	}
	if req.Env["HOME"] != home {
		t.Fatalf("env = %#v", req.Env)
	}

	cfg := applyConfig(req)
	if cfg.User != "0:0" {
		t.Fatalf("user = %q", cfg.User)
	}
	if !slices.Equal(cfg.Entrypoint, []string{inst.TobyBinaryPath()}) {
		t.Fatalf("entrypoint = %#v", cfg.Entrypoint)
	}
	if !cfg.OpenStdin || !cfg.AttachStdin {
		t.Fatalf("run config should keep stdin open: %#v", cfg)
	}
	if cfg.Tty {
		t.Fatal("the gRPC stdio link must not use a TTY")
	}

	hc := applyHostConfig(req)
	if hc.NetworkMode == "host" {
		t.Fatal("run container must use bridge networking, not host")
	}
	if hc.Init == nil || !*hc.Init {
		t.Fatal("run should request an init process")
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

func TestContainerRequestMultiMountsSetupAndFinal(t *testing.T) {
	inst, _ := dockerInstance(t, nil)
	home := inst.HomeDir()
	m := mount.Entry{
		Volume:    "toby.demo.tool.opencode.config",
		Target:    filepath.Join(home, ".config", "opencode"),
		SetupPath: filepath.Join(layout.Root, "mounts", "opencode-config"),
	}
	req := inst.containerRequest(RunSpec{Env: environ.Environment{"HOME": home}, Mounts: []mount.Entry{m}})
	hc := applyHostConfig(req)
	if v, ok := findMount(hc.Mounts, m.SetupPath); !ok || v.Source != m.Volume {
		t.Fatalf("volume must be mounted at its setup path: %#v ok=%v", v, ok)
	}
	if v, ok := findMount(hc.Mounts, m.Target); !ok || v.Source != m.Volume {
		t.Fatalf("volume must also be mounted at its final target: %#v ok=%v", v, ok)
	}
}

func TestContainerRequestDebugNamesContainer(t *testing.T) {
	inst, _ := dockerInstance(t, nil)
	spec := RunSpec{Env: environ.Environment{}, Mounts: dockerHomeMount(inst.HomeDir()), Debug: true}
	if got := inst.containerRequest(spec).Name; got != inst.containerName+"-run" {
		t.Fatalf("debug name = %q, want %q", got, inst.containerName+"-run")
	}
	if got := inst.containerRequest(RunSpec{Env: environ.Environment{}}).Name; got != "" {
		t.Fatalf("non-debug name = %q, want empty", got)
	}
}

func TestNewInstanceAppliesImage(t *testing.T) {
	inst, _ := dockerInstance(t, func(s *Spec) {
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
