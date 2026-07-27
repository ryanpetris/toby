//go:build linux

package run

// Covers the application sandbox's host resolver capability.

import (
	"testing"

	"petris.dev/toby/internal/sandbox/bwrap"
	"petris.dev/toby/internal/sandbox/mount"
)

func TestAddNativeResolverBind(t *testing.T) {
	sandbox, err := bwrap.NewToolSandbox(bwrap.ToolSandboxOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if err := addNativeResolverBind(sandbox); err != nil {
		t.Fatal(err)
	}

	binds := sandbox.Binds()
	if len(binds) != 1 {
		t.Fatalf("binds = %#v", binds)
	}
	if got := binds[0]; got.HostPath != nativeResolverPath ||
		got.Target != nativeResolverPath ||
		got.Access != mount.AccessReadOnly {
		t.Fatalf("resolver bind = %#v", got)
	}
}
