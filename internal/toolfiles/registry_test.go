package toolfiles

// Verifies registration validation, target collision rejection, deterministic
// ordering, and detached file contents.

import (
	"reflect"
	"testing"

	"petris.dev/toby/internal/configpatch"
)

func TestRegistryReturnsDeterministicDetachedFiles(t *testing.T) {
	registry := NewRegistry()
	last := File{
		Owner:  "opencode",
		Target: "/toby/home/.config/opencode/opencode.json",
		Data:   []byte("opencode"),
		Mode:   0o600,
		UID:    1000,
		GID:    1000,
	}
	first := File{
		Owner:  "codex",
		Target: "/toby/home/.codex/config.toml",
		Data:   []byte("codex"),
		Mode:   0o600,
		UID:    1000,
		GID:    1000,
	}

	if err := registry.Register(last); err != nil {
		t.Fatal(err)
	}
	if err := registry.Register(first); err != nil {
		t.Fatal(err)
	}
	last.Data[0] = 'X'
	first.Data[0] = 'X'

	got := registry.Files()
	if targets := []string{got[0].Target, got[1].Target}; !reflect.DeepEqual(
		targets,
		[]string{first.Target, last.Target},
	) {
		t.Fatalf("targets = %q, want deterministic target order", targets)
	}
	if string(got[0].Data) != "codex" || string(got[1].Data) != "opencode" {
		t.Fatalf("registered data aliases caller buffers: %q, %q", got[0].Data, got[1].Data)
	}

	got[0].Data[0] = 'X'
	if next := registry.Files(); string(next[0].Data) != "codex" {
		t.Fatalf("Files result aliases registry data: %q", next[0].Data)
	}
}

func TestRegistryClonesPatchIntents(t *testing.T) {
	registry := NewRegistry()
	file := File{
		Owner:  "grok",
		Target: "/toby/home/.grok/config.toml",
		Patch: configpatch.Patch{
			Ensure: []configpatch.Value{{
				Path:  "/plugins/enabled",
				Value: map[string]any{"name": "toby-session"},
			}},
		},
		Mode: 0o600,
		UID:  1000,
		GID:  1000,
	}
	if err := registry.Register(file); err != nil {
		t.Fatal(err)
	}
	file.Patch.Ensure[0].Value.(map[string]any)["name"] = "mutated"

	got := registry.Files()
	if got[0].Patch.Ensure[0].Value.(map[string]any)["name"] != "toby-session" {
		t.Fatalf("registered patch aliases caller value: %#v", got[0].Patch)
	}
	got[0].Patch.Ensure[0].Value.(map[string]any)["name"] = "mutated"
	if next := registry.Files(); next[0].Patch.Ensure[0].Value.(map[string]any)["name"] != "toby-session" {
		t.Fatalf("Files result aliases registry patch: %#v", next[0].Patch)
	}
}

func TestRegistryRejectsInvalidAndOverlappingTargets(t *testing.T) {
	valid := File{
		Owner:  "codex",
		Target: "/toby/home/.codex/config.toml",
		Data:   []byte("config"),
		Mode:   0o600,
		UID:    1000,
		GID:    1000,
	}

	tests := []struct {
		name   string
		mutate func(*File)
	}{
		{
			name: "empty owner",
			mutate: func(file *File) {
				file.Owner = ""
			},
		},
		{
			name: "owner path",
			mutate: func(file *File) {
				file.Owner = "../codex"
			},
		},
		{
			name: "relative target",
			mutate: func(file *File) {
				file.Target = ".codex/config.toml"
			},
		},
		{
			name: "traversal",
			mutate: func(file *File) {
				file.Target = "/toby/home/.codex/../escape"
			},
		},
		{
			name: "type bits",
			mutate: func(file *File) {
				file.Mode = 0o600 | 1<<31
			},
		},
		{
			name: "empty permissions",
			mutate: func(file *File) {
				file.Mode = 0
			},
		},
		{
			name: "no owner access",
			mutate: func(file *File) {
				file.Mode = 0o044
			},
		},
		{
			name: "negative uid",
			mutate: func(file *File) {
				file.UID = -1
			},
		},
		{
			name: "negative gid",
			mutate: func(file *File) {
				file.GID = -1
			},
		},
		{
			name: "data and patch",
			mutate: func(file *File) {
				file.Patch = configpatch.Patch{
					Ensure: []configpatch.Value{{
						Path:  "/plugins/enabled",
						Value: "toby-session",
					}},
				}
			},
		},
		{
			name: "patch target extension",
			mutate: func(file *File) {
				file.Data = nil
				file.Target = "/toby/home/.grok/config"
				file.Patch = configpatch.Patch{
					Ensure: []configpatch.Value{{
						Path:  "/plugins/enabled",
						Value: "toby-session",
					}},
				}
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			registry := NewRegistry()
			file := valid
			test.mutate(&file)

			if err := registry.Register(file); err == nil {
				t.Fatal("invalid native tool file was accepted")
			}
			if got := registry.Files(); len(got) != 0 {
				t.Fatalf("registry retained %d files after rejection", len(got))
			}
		})
	}

	for _, target := range []string{
		valid.Target,
		valid.Target + "/nested",
		"/toby/home/.codex",
	} {
		t.Run("overlap "+target, func(t *testing.T) {
			registry := NewRegistry()
			if err := registry.Register(valid); err != nil {
				t.Fatal(err)
			}

			conflict := valid
			conflict.Owner = "other"
			conflict.Target = target
			if err := registry.Register(conflict); err == nil {
				t.Fatal("overlapping native tool-file target was accepted")
			}
			if got := registry.Files(); len(got) != 1 {
				t.Fatalf("registry retained %d files after collision", len(got))
			}
		})
	}
}
