package sandbox

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"petris.dev/toby/internal/control"
	sandboxmount "petris.dev/toby/internal/sandbox/mount"
	sandboxpath "petris.dev/toby/internal/sandbox/path"
	"petris.dev/toby/internal/tools/helpers"
	"petris.dev/toby/internal/tools/tool"
)

func TestCommandExitResultReturnsExitCodeErrors(t *testing.T) {
	code, err := commandExitResult(control.CommandExitParams{ExitCode: 7})
	var coded interface{ ExitCode() int }
	if code != 7 || !errors.As(err, &coded) || coded.ExitCode() != 7 || err.Error() != "" {
		t.Fatalf("code = %d, err = %#v, coded = %#v", code, err, coded)
	}
}

func TestCommandExitResultReturnsCommandErrors(t *testing.T) {
	code, err := commandExitResult(control.CommandExitParams{ExitCode: 127, Error: "not found"})
	var coded interface{ ExitCode() int }
	if code != 127 || !errors.As(err, &coded) || coded.ExitCode() != 127 || err.Error() != "not found" {
		t.Fatalf("code = %d, err = %#v, coded = %#v", code, err, coded)
	}
}

func TestCommandExitResultSuccess(t *testing.T) {
	code, err := commandExitResult(control.CommandExitParams{})
	if code != 0 || err != nil {
		t.Fatalf("code = %d, err = %v", code, err)
	}
}

func TestServiceTracksBindsUntilSandboxStarts(t *testing.T) {
	svc := newService(sandboxmount.NewService())
	bind := sandboxmount.Bind{HostPath: "/host/state", Target: helpers.HomeTarget(".state")}
	if err := svc.AddBind(bind); err != nil {
		t.Fatal(err)
	}
	if err := svc.AddBind(bind); err != nil {
		t.Fatal(err)
	}
	if got, want := svc.StartBinds(), []sandboxmount.Bind{bind}; !reflect.DeepEqual(got, want) {
		t.Fatalf("binds = %#v, want %#v", got, want)
	}
	if err := svc.AddBind(sandboxmount.Bind{HostPath: "/host/other", Target: helpers.HomeTarget(".other")}); err == nil {
		t.Fatal("expected AddBind after start to fail")
	}
	svc.Prepare(nil)
	if err := svc.AddBind(sandboxmount.Bind{HostPath: "/host/other", Target: helpers.HomeTarget(".other")}); err != nil {
		t.Fatal(err)
	}
}

func TestServiceTracksManagedMountsUntilSandboxStarts(t *testing.T) {
	svc := newService(sandboxmount.NewService())
	if err := svc.mounts.Configure(sandboxmount.Config{Profile: "default", SandboxName: "demo", Paths: sandboxpath.Defaults()}); err != nil {
		t.Fatal(err)
	}
	req := sandboxmount.Request{Key: sandboxmount.Key{Type: sandboxmount.TypeTool, Name: "opencode", Purpose: "config"}, Target: sandboxpath.HomePath(".config", "opencode")}
	mount, err := svc.AddMount(req)
	if err != nil {
		t.Fatal(err)
	}
	if mount.SetupPath == "" || mount.SetupPath == mount.Target || mount.ProviderID != "toby.default.tool.opencode.config" {
		t.Fatalf("mount = %#v", mount)
	}
	again, err := svc.AddMount(req)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(again, mount) {
		t.Fatalf("deduped mount = %#v, want %#v", again, mount)
	}
	if _, err := svc.AddMount(sandboxmount.Request{Key: req.Key, Target: sandboxpath.HomePath("other")}); err == nil {
		t.Fatal("expected conflicting persistent mount target to fail")
	}
	homeKey := sandboxmount.RuntimeHomeKey("demo")
	if got, want := svc.RuntimeMounts(), []sandboxmount.RuntimeMount{{Key: homeKey, ProviderID: "toby.default.runtime.home.demo", Source: sandboxmount.Source{Kind: sandboxmount.SourceProvider, Value: "toby.default.runtime.home.demo"}, Target: sandboxpath.DefaultHome, SetupPath: gotSetupPath(t, svc, homeKey), Access: sandboxmount.AccessRegular}, {Key: mount.Key, ProviderID: mount.ProviderID, Source: mount.Source, Target: mount.Target, SetupPath: mount.SetupPath, Access: mount.Access}}; !reflect.DeepEqual(got, want) {
		t.Fatalf("mounts = %#v, want %#v", got, want)
	}
	_ = svc.StartBinds()
	if _, err := svc.AddMount(sandboxmount.Request{Key: sandboxmount.Key{Type: sandboxmount.TypeTool, Name: "opencode", Purpose: "data"}, Target: sandboxpath.HomePath(".local", "share", "opencode")}); err == nil {
		t.Fatal("expected AddMount after start to fail")
	}
}

func TestMountHostInitHookPreparesHostBackedMounts(t *testing.T) {
	home := t.TempDir()
	mounts := sandboxmount.NewService()
	if err := mounts.Configure(sandboxmount.Config{Profile: "default", SandboxName: "demo", Paths: sandboxpath.Defaults(), Profiles: sandboxmount.Profiles{"default": {Backing: sandboxmount.BackingHost, HostRoot: filepath.Join(home, "state")}}}); err != nil {
		t.Fatal(err)
	}
	mount, err := mounts.Add(sandboxmount.Request{Key: sandboxmount.Key{Type: sandboxmount.TypeTool, Name: tool.OpenCodeToolName, Purpose: "config"}, Target: sandboxpath.HomePath(".config", "opencode")})
	if err != nil {
		t.Fatal(err)
	}
	hook := provideLifecycleHooks(mounts).HostInit
	if hook.Name != "sandbox.mounts.prepare-host" || hook.Owner != "" || hook.Priority <= 0 || hook.Run == nil {
		t.Fatalf("hook = %#v", hook)
	}
	if err := hook.Run(context.Background(), tool.LifecycleContext{}); err != nil {
		t.Fatal(err)
	}
	if info, err := os.Stat(mount.Source.Value); err != nil || !info.IsDir() {
		t.Fatalf("prepared source = %#v, %v", info, err)
	}
}

func gotSetupPath(t *testing.T, svc *Service, key sandboxmount.Key) string {
	t.Helper()
	info, ok := svc.Mount(key)
	if !ok {
		t.Fatalf("missing mount %s", key.String())
	}
	return info.SetupPath
}
