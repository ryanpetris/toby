package docker

// Tests Docker tool relay configuration and launch behavior.

import (
	"context"
	"net"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"petris.dev/toby/internal/config"
	sandboxapi "petris.dev/toby/internal/sandbox"
	"petris.dev/toby/internal/sandbox/layout"
	"petris.dev/toby/internal/sandbox/mount"
	"petris.dev/toby/internal/tools"
	"petris.dev/toby/internal/tools/fake"
)

func TestProvideUsesOnlyHostDockerCapabilities(t *testing.T) {
	home := t.TempDir()
	sandbox := fake.NewSandbox()
	sandbox.Env["DOCKER_CONTEXT"] = "inherited"
	svc := provide(config.Paths{Home: home}, sandbox).Service
	socket := filepath.Join(t.TempDir(), "docker.sock")
	listener, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = listener.Close() })
	svc.(*dockerTool).socket = socket

	if svc.Name() != Name || svc.CommandName() != Name || svc.LaunchHelp() == "" {
		t.Fatalf("metadata = name %q command %q help %q", svc.Name(), svc.CommandName(), svc.LaunchHelp())
	}
	if err := svc.PrepareHost(context.Background(), &tools.Options{}); err != nil {
		t.Fatal(err)
	}
	want := []mount.Bind{{
		HostPath: filepath.Join(home, ".docker"),
		Target:   filepath.Join(layout.Home, ".docker"),
		Access:   mount.AccessReadOnly,
		Optional: true,
	}}
	if !reflect.DeepEqual(sandbox.Binds, want) {
		t.Fatalf("Binds = %#v, want %#v", sandbox.Binds, want)
	}
	if len(sandbox.Mounts) != 0 {
		t.Fatalf("Docker registered Toby-managed volumes: %#v", sandbox.Mounts)
	}
	if err := svc.ConfigureSandbox(t.Context()); err != nil {
		t.Fatal(err)
	}
	if got := sandbox.Env["DOCKER_HOST"]; got != "unix://"+sandboxSocketPath {
		t.Fatalf("DOCKER_HOST = %q", got)
	}
	if _, found := sandbox.Env["DOCKER_CONTEXT"]; found {
		t.Fatal("inherited DOCKER_CONTEXT was retained")
	}

	relays, err := svc.(*dockerTool).SocketRelays()
	if err != nil {
		t.Fatal(err)
	}
	if len(relays) != 1 ||
		relays[0].HostSocket != socket ||
		relays[0].SandboxSocket != sandboxSocketPath {
		t.Fatalf("SocketRelays = %#v", relays)
	}
}

func TestPrepareHostRequiresUnixSocket(t *testing.T) {
	svc := provide(
		config.Paths{Home: t.TempDir()},
		fake.NewSandbox(),
	).Service.(*dockerTool)
	svc.socket = filepath.Join(t.TempDir(), "missing.sock")

	err := svc.PrepareHost(t.Context(), &tools.Options{})
	if err == nil || !strings.Contains(err.Error(), "requires host socket") {
		t.Fatalf("missing socket error = %v", err)
	}
}

func TestInstallRequiresDockerCLI(t *testing.T) {
	sandbox := fake.NewSandbox()
	sandbox.ExecFunc = func(
		context.Context,
		[]string,
		sandboxapi.ExecOptions,
	) (int, error) {
		return 1, nil
	}
	svc := provide(config.Paths{Home: t.TempDir()}, sandbox).Service

	err := svc.Install(t.Context(), false)
	if err == nil || !strings.Contains(err.Error(), "requires the Docker CLI") {
		t.Fatalf("missing CLI error = %v", err)
	}
}

func TestLaunchRunsDockerWithExtras(t *testing.T) {
	var got []string
	sandbox := fake.NewSandbox()
	sandbox.ExecFunc = func(_ context.Context, argv []string, _ sandboxapi.ExecOptions) (int, error) {
		got = append([]string(nil), argv...)
		return 0, nil
	}
	svc := provide(config.Paths{Home: t.TempDir()}, sandbox).Service

	if err := svc.Launch(context.Background(), []string{"ps", "--format", "json"}); err != nil {
		t.Fatal(err)
	}
	want := []string{"docker", "ps", "--format", "json"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("argv = %#v, want %#v", got, want)
	}
}
