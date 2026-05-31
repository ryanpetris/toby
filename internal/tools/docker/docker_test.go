package docker

import (
	"context"
	"path/filepath"
	"reflect"
	"testing"

	"petris.dev/toby/internal/config"
	"petris.dev/toby/internal/tools/tool"
	"petris.dev/toby/internal/tools/tooltest"
)

func TestProvideMetadataAndBinds(t *testing.T) {
	home := t.TempDir()
	svc := Provide(config.Paths{Home: home}, tooltest.NewSandbox("/toby/context")).Service

	if svc.Name() != tool.DockerToolName || svc.CommandName() != tool.DockerToolName || svc.LaunchHelp() == "" {
		t.Fatalf("metadata = name %q command %q help %q", svc.Name(), svc.CommandName(), svc.LaunchHelp())
	}
	want := []tool.Bind{
		{HostPath: filepath.Join(home, ".docker"), Target: tool.HomeTarget(".docker"), Type: tool.BindReadOnly, Optional: true, State: true},
		{HostPath: "/var/run/docker.sock", Target: tool.AbsoluteTarget("/var/run/docker.sock"), Type: tool.BindDev, Optional: true},
	}
	if got := svc.Binds(); !reflect.DeepEqual(got, want) {
		t.Fatalf("Binds = %#v, want %#v", got, want)
	}
}

func TestLaunchRunsDockerWithExtras(t *testing.T) {
	var got []string
	sandbox := tooltest.NewSandbox("/toby/context")
	sandbox.ExecFunc = func(_ context.Context, argv []string, _ tool.ExecOptions) (int, error) {
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
