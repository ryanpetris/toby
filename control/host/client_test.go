package host

import (
	"os"
	"testing"

	"petris.dev/toby/control"
	"petris.dev/toby/control/methods/command"
)

func TestResolveOwnerHostSentinels(t *testing.T) {
	uid, gid, err := resolveOwner(control.HostUser, control.HostGroup)
	if err != nil {
		t.Fatal(err)
	}
	if uid != os.Getuid() || gid != os.Getgid() {
		t.Fatalf("owner = %d:%d, want %d:%d", uid, gid, os.Getuid(), os.Getgid())
	}
	if _, _, err := resolveOwner(-1, 0); err == nil {
		t.Fatal("expected invalid uid to fail")
	}
}

func TestResolveCommandRunParamsHostIdentity(t *testing.T) {
	params, err := resolveCommandRunParams(command.RunParams{UID: control.HostUser, GID: control.HostGroup})
	if err != nil {
		t.Fatal(err)
	}
	if params.UID != os.Getuid() || params.GID != os.Getgid() {
		t.Fatalf("command identity = %#v, want uid=%d gid=%d", params, os.Getuid(), os.Getgid())
	}
	hostGroups, err := os.Getgroups()
	if err != nil {
		t.Fatal(err)
	}
	if len(params.Groups) != len(hostGroups) {
		t.Fatalf("groups = %#v, want %#v", params.Groups, hostGroups)
	}
}
