package helpers

import (
	"path/filepath"
	"testing"

	sandboxmount "petris.dev/toby/internal/sandbox/mount"
)

func TestParseMountBacking(t *testing.T) {
	if backing, err := ParseMountBacking(" host "); err != nil || backing != sandboxmount.BackingHost {
		t.Fatalf("ParseMountBacking host = %q, %v", backing, err)
	}
	if backing, err := ParseMountBacking("private"); err != nil || backing != sandboxmount.BackingPrivate {
		t.Fatalf("ParseMountBacking private = %q, %v", backing, err)
	}
	if backing, err := ParseMountBacking("default"); err != nil || backing != sandboxmount.BackingDefault {
		t.Fatalf("ParseMountBacking default = %q, %v", backing, err)
	}
	if backing, err := ParseMountBacking("provider"); err != nil || backing != sandboxmount.BackingProvider {
		t.Fatalf("ParseMountBacking provider = %q, %v", backing, err)
	}
	if _, err := ParseMountBacking("shared"); err == nil {
		t.Fatal("expected invalid backing to fail")
	}
}

func TestResolveMountHostRoot(t *testing.T) {
	home := filepath.Join(string(filepath.Separator), "home", "demo")
	base := filepath.Join(home, "project")
	tests := []struct {
		name    string
		value   string
		want    string
		wantErr bool
	}{
		{name: "home", value: "~/state", want: filepath.Join(home, "state")},
		{name: "absolute", value: "/tmp/state", want: "/tmp/state"},
		{name: "relative", value: "state", want: filepath.Join(base, "state")},
		{name: "empty", value: " ", wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ResolveMountHostRoot(tt.value, home, base)
			if tt.wantErr {
				if err == nil {
					t.Fatal("expected error")
				}
				return
			}
			if err != nil || got != tt.want {
				t.Fatalf("ResolveMountHostRoot = %q, %v; want %q", got, err, tt.want)
			}
		})
	}
}
