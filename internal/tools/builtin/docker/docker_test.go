package docker

import (
	"context"
	"path/filepath"
	"reflect"
	"testing"

	"petris.dev/toby/config"
	"petris.dev/toby/container/layout"
	"petris.dev/toby/container/mount"
	"petris.dev/toby/internal/tools/fake"
	"petris.dev/toby/tools"
)

func TestProvideMetadataAndHostInitBinds(t *testing.T) {
	home := t.TempDir()
	sandbox := fake.NewSandbox("/toby/context")
	svc := Provide(config.Paths{Home: home}, sandbox).Service

	if svc.Name() != Name || svc.CommandName() != Name || svc.LaunchHelp() == "" {
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
}

func TestLaunchRunsDockerWithExtras(t *testing.T) {
	sandbox := fake.NewSandbox("/toby/context")
	svc := Provide(config.Paths{Home: t.TempDir()}, sandbox).Service

	got, err := svc.LaunchCommand(context.Background(), []string{"ps", "--format", "json"})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"docker", "ps", "--format", "json"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("argv = %#v, want %#v", got, want)
	}
}
