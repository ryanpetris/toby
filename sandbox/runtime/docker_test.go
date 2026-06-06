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
	"github.com/moby/moby/api/types/network"
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

func TestContainerRequestRunsIdle(t *testing.T) {
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
	if !slices.Equal(req.Cmd, []string{"sandbox", "idle"}) {
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
	if cfg.Tty {
		t.Fatal("the idle main process must not use a TTY")
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

func TestContainerRequestPublishesPorts(t *testing.T) {
	inst, _ := dockerInstance(t, func(s *Spec) {
		s.Ports = []string{"8080:3000", "127.0.0.1:9090:9090/udp"}
	})
	home := inst.HomeDir()
	req := inst.containerRequest(RunSpec{Env: environ.Environment{"HOME": home}})

	tcp3000 := network.MustParsePort("3000/tcp")
	udp9090 := network.MustParsePort("9090/udp")

	cfg := applyConfig(req)
	if _, ok := cfg.ExposedPorts[tcp3000]; !ok {
		t.Fatalf("exposed ports missing 3000/tcp: %#v", cfg.ExposedPorts)
	}
	if _, ok := cfg.ExposedPorts[udp9090]; !ok {
		t.Fatalf("exposed ports missing 9090/udp: %#v", cfg.ExposedPorts)
	}

	hc := applyHostConfig(req)
	if b := hc.PortBindings[tcp3000]; len(b) != 1 || b[0].HostPort != "8080" || b[0].HostIP.IsValid() {
		t.Fatalf("3000/tcp binding = %#v", hc.PortBindings[tcp3000])
	}
	if b := hc.PortBindings[udp9090]; len(b) != 1 || b[0].HostPort != "9090" || b[0].HostIP.String() != "127.0.0.1" {
		t.Fatalf("9090/udp binding = %#v", hc.PortBindings[udp9090])
	}
}

func TestContainerRequestWithoutPortsPublishesNone(t *testing.T) {
	inst, _ := dockerInstance(t, nil)
	req := inst.containerRequest(RunSpec{Env: environ.Environment{"HOME": inst.HomeDir()}})
	if got := applyConfig(req).ExposedPorts; len(got) != 0 {
		t.Fatalf("exposed ports = %#v, want none", got)
	}
	if got := applyHostConfig(req).PortBindings; len(got) != 0 {
		t.Fatalf("port bindings = %#v, want none", got)
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
