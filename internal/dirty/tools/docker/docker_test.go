package docker

import (
	"context"
	"path/filepath"
	"reflect"
	"testing"

	"petris.dev/toby/config"
	"petris.dev/toby/container/layout"
	"petris.dev/toby/container/mount"
	sandboxapi "petris.dev/toby/sandbox"
	"petris.dev/toby/tools"
	"petris.dev/toby/tools/tooltest"
)

func TestProvideMetadataAndHostInitBinds(t *testing.T) {
	home := t.TempDir()
	sandbox := tooltest.NewSandbox("/toby/context")
	svc := Provide(config.Paths{Home: home}, sandbox).Service

	if svc.Name() != tools.DockerToolName || svc.CommandName() != tools.DockerToolName || svc.LaunchHelp() == "" {
		t.Fatalf("metadata = name %q command %q help %q", svc.Name(), svc.CommandName(), svc.LaunchHelp())
	}
	if err := svc.PrepareHost(context.Background(), &tools.Options{}); err != nil {
		t.Fatal(err)
	}
	want := []mount.Bind{
		{HostPath: filepath.Join(home, ".docker"), Target: filepath.Join(layout.Home, ".docker"), Access: mount.AccessReadOnly, Optional: true},
		{HostPath: layout.DockerSocket, Target: layout.DockerSocket, Access: mount.AccessDev, Optional: true},
	}
	if !reflect.DeepEqual(sandbox.Binds, want) {
		t.Fatalf("Binds = %#v, want %#v", sandbox.Binds, want)
	}
	if len(sandbox.Mounts) != 0 {
		t.Fatalf("Mounts = %#v", sandbox.Mounts)
	}
}

func TestLaunchRunsDockerWithExtras(t *testing.T) {
	var got []string
	sandbox := tooltest.NewSandbox("/toby/context")
	sandbox.ExecFunc = func(_ context.Context, argv []string, _ sandboxapi.ExecOptions) (int, error) {
		got = append([]string(nil), argv...)
		return 0, nil
	}
	svc := Provide(config.Paths{Home: t.TempDir()}, sandbox).Service

	if err := svc.Launch(context.Background(), []string{"ps", "--format", "json"}); err != nil {
		t.Fatal(err)
	}
	want := []string{"docker", "ps", "--format", "json"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("argv = %#v, want %#v", got, want)
	}
}
