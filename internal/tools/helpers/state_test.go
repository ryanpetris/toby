package helpers

import (
	"path/filepath"
	"testing"

	"petris.dev/toby/internal/tools/tool"
)

func TestParseToolState(t *testing.T) {
	if state, err := ParseToolState(" host "); err != nil || state != tool.ToolStateHost {
		t.Fatalf("ParseToolState host = %q, %v", state, err)
	}
	if state, err := ParseToolState("private"); err != nil || state != tool.ToolStatePrivate {
		t.Fatalf("ParseToolState private = %q, %v", state, err)
	}
	if _, err := ParseToolState("shared"); err == nil {
		t.Fatal("expected invalid state to fail")
	}
}

func TestResolveStateRoot(t *testing.T) {
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
			got, err := ResolveStateRoot(tt.value, home, base)
			if tt.wantErr {
				if err == nil {
					t.Fatal("expected error")
				}
				return
			}
			if err != nil || got != tt.want {
				t.Fatalf("ResolveStateRoot = %q, %v; want %q", got, err, tt.want)
			}
		})
	}
}
