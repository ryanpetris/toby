package mount

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"

	sandboxpath "petris.dev/toby/internal/sandbox/path"
)

func TestProviderIDMatchesDockerVolumeName(t *testing.T) {
	key := Key{Type: TypeTool, Name: "opencode", Purpose: "config"}
	if got, want := ProviderID("default", key), "toby.default.tool.opencode.config"; got != want {
		t.Fatalf("provider id = %q, want %q", got, want)
	}
}

func TestServiceResolvesProviderHostPrivateAndRuntimeHome(t *testing.T) {
	home := t.TempDir()
	svc := NewService()
	err := svc.Configure(Config{
		Profile:      "default",
		SandboxName:  "demo",
		Paths:        sandboxpath.Defaults(),
		ToolProfiles: map[string]string{"opencode": "host", "claude": "private"},
		Profiles: Profiles{
			"default": {Backing: BackingProvider, HostRoot: filepath.Join(home, "default")},
			"host":    {Backing: BackingHost, HostRoot: filepath.Join(home, "opencode")},
			"private": {Backing: BackingPrivate},
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	homeKey := RuntimeHomeKey("demo")
	if info, ok := svc.Get(homeKey); !ok || info.Backing != BackingProvider || info.ProviderID != "toby.default.runtime.home.demo" {
		t.Fatalf("runtime home = %#v, ok=%v", info, ok)
	}
	opencode, err := svc.Add(Request{Key: Key{Type: TypeTool, Name: "opencode", Purpose: "config"}, Target: sandboxpath.HomePath(".config", "opencode")})
	if err != nil {
		t.Fatal(err)
	}
	if opencode.Backing != BackingHost || opencode.Source.Kind != SourceHostPath || opencode.Source.Value != filepath.Join(home, "opencode", ".config", "opencode") {
		t.Fatalf("opencode = %#v", opencode)
	}
	claude, err := svc.Add(Request{Key: Key{Type: TypeTool, Name: "claude", Purpose: "state"}, Target: sandboxpath.HomePath(".config", "claude")})
	if err != nil {
		t.Fatal(err)
	}
	if claude.Active || claude.Backing != BackingPrivate {
		t.Fatalf("claude = %#v", claude)
	}

	got := svc.RuntimeMounts()
	if len(got) != 2 || got[0].Key != homeKey || got[1].Key != opencode.Key {
		t.Fatalf("runtime mounts = %#v", got)
	}
	if hostBacked := svc.HostBackedManagedMounts(); !reflect.DeepEqual(hostBacked, []Info{opencode}) {
		t.Fatalf("host backed = %#v, want %#v", hostBacked, []Info{opencode})
	}
	if _, err := os.Stat(opencode.Source.Value); !os.IsNotExist(err) {
		t.Fatalf("host-backed source exists before prepare: %v", err)
	}
	if err := svc.PrepareHostMounts(); err != nil {
		t.Fatal(err)
	}
	if info, err := os.Stat(opencode.Source.Value); err != nil || !info.IsDir() {
		t.Fatalf("prepared source = %#v, %v", info, err)
	}
}
