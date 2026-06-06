package runtime

import (
	"reflect"
	"testing"

	"petris.dev/toby/container/layout"
	"petris.dev/toby/container/mount"
)

func TestServiceTracksBindsUntilSandboxStarts(t *testing.T) {
	svc := newService(mount.New())
	bind := mount.Bind{HostPath: "/host/state", Target: "~/.state"}
	if err := svc.AddBind(bind); err != nil {
		t.Fatal(err)
	}
	if err := svc.AddBind(bind); err != nil {
		t.Fatal(err)
	}
	want := mount.Bind{HostPath: "/host/state", Target: layout.Expand("~/.state"), Access: mount.AccessRegular}
	if got := svc.StartBinds(); !reflect.DeepEqual(got, []mount.Bind{want}) {
		t.Fatalf("binds = %#v, want %#v", got, []mount.Bind{want})
	}
	if err := svc.AddBind(mount.Bind{HostPath: "/host/other", Target: "~/.other"}); err == nil {
		t.Fatal("expected AddBind after start to fail")
	}
	svc.Prepare(nil)
	if err := svc.AddBind(mount.Bind{HostPath: "/host/other", Target: "~/.other"}); err != nil {
		t.Fatal(err)
	}
}

func TestServiceTracksManagedMountsUntilSandboxStarts(t *testing.T) {
	svc := newService(mount.New())
	if err := svc.mounts.Configure(mount.Config{Profile: "default", SandboxName: "demo"}); err != nil {
		t.Fatal(err)
	}
	req := mount.Request{Key: mount.Key{Type: mount.TypeTool, Name: "opencode", Purpose: "config"}, Target: "~/.config/opencode"}
	mnt, err := svc.AddMount(req)
	if err != nil {
		t.Fatal(err)
	}
	if mnt.SetupPath == "" || mnt.SetupPath == mnt.Target || mnt.Volume != "toby.default.tool.opencode.config" {
		t.Fatalf("mount = %#v", mnt)
	}
	again, err := svc.AddMount(req)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(again, mnt) {
		t.Fatalf("deduped mount = %#v, want %#v", again, mnt)
	}
	if _, err := svc.AddMount(mount.Request{Key: req.Key, Target: "~/other"}); err == nil {
		t.Fatal("expected conflicting persistent mount target to fail")
	}
	homeKey := mount.RuntimeHomeKey("demo")
	homeMount, ok := svc.Mount(homeKey)
	if !ok {
		t.Fatal("missing runtime home mount")
	}
	if homeMount.Volume != "toby.default.runtime.home.demo" || homeMount.Target != layout.Home {
		t.Fatalf("home mount = %#v", homeMount)
	}
	if got, want := svc.RuntimeMounts(), []mount.Entry{homeMount, mnt}; !reflect.DeepEqual(got, want) {
		t.Fatalf("mounts = %#v, want %#v", got, want)
	}
	_ = svc.StartBinds()
	if _, err := svc.AddMount(mount.Request{Key: mount.Key{Type: mount.TypeTool, Name: "opencode", Purpose: "data"}, Target: "~/.local/share/opencode"}); err == nil {
		t.Fatal("expected AddMount after start to fail")
	}
}
