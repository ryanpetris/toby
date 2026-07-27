package bwrap

// Verifies deterministic cloning and validation of Bubblewrap run plans.

import (
	"path"
	"path/filepath"
	"reflect"
	"testing"

	"petris.dev/toby/internal/sandbox/layout"
	"petris.dev/toby/internal/sandbox/mount"
)

func TestPlanCanonicalIsDeterministicAndDetached(t *testing.T) {
	first := validPlan()
	first.Projects = []Project{
		{Name: "z", HostPath: "/projects/z", Target: "/toby/workspace/z"},
		{Name: "a", HostPath: "/projects/a", Target: "/toby/workspace/a"},
	}
	first.Environment = []EnvironmentVariable{{Name: "Z", Value: "last"}, {Name: "A", Value: "first"}}

	second := validPlan()
	second.Projects = []Project{first.Projects[1], first.Projects[0]}
	second.Environment = []EnvironmentVariable{first.Environment[1], first.Environment[0]}

	gotFirst := first.Canonical()
	gotSecond := second.Canonical()
	if !reflect.DeepEqual(gotFirst, gotSecond) {
		t.Fatalf("canonical plans differ:\n%#v\n%#v", gotFirst, gotSecond)
	}

	first.Command.Argv[0] = "mutated"
	first.GeneratedFiles[0].Data[0] = 'X'
	if gotFirst.Command.Argv[0] != "sh" || string(gotFirst.GeneratedFiles[0].Data) != "data" {
		t.Fatal("canonical plan aliases input slices")
	}
}

func TestPlanValidate(t *testing.T) {
	plan := validPlan()
	if err := plan.Validate(); err != nil {
		t.Fatal(err)
	}

	plan.Command.Argv = []string{"sh", "-c", "echo unsafe"}
	if err := plan.Validate(); err != nil {
		t.Fatalf("argv is data, not a shell construction: %v", err)
	}

	plan.ManagedDirectories = append(plan.ManagedDirectories, mount.Entry{
		Key:      mount.Key{Type: mount.TypeTool, Name: "nested", Purpose: "state"},
		Profile:  "default",
		HostPath: "/data/volumes/nested/_data",
		Target:   "/toby/home/.local/share/opencode/cache",
		Access:   mount.AccessRegular,
	})
	if err := plan.Validate(); err == nil {
		t.Fatal("overlapping managed-directory targets were accepted")
	}
}

func TestPlanRejectsSharedOverlayPath(t *testing.T) {
	plan := validPlan()
	plan.Overlay.Work = plan.Overlay.Upper
	if err := plan.Validate(); err == nil {
		t.Fatal("shared upper/work path was accepted")
	}
}

func TestPlanRequiresCanonicalSHA256RootFSDigest(t *testing.T) {
	for _, digest := range []string{
		"sha256:0123456789abcdef",
		"sha512:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef",
		"sha256:0123456789ABCDEF0123456789abcdef0123456789abcdef0123456789abcdef",
	} {
		plan := validPlan()
		plan.RootFS.Digest = digest
		if err := plan.Validate(); err == nil {
			t.Fatalf("noncanonical digest %q was accepted", digest)
		}
	}
}

func TestPlanRequiresHostNetworking(t *testing.T) {
	plan := validPlan()
	plan.Namespaces.Network = NetworkMode("private")
	if err := plan.Validate(); err == nil {
		t.Fatal("private network mode was accepted")
	}
}

func TestPlanRequiresExplicitConsistentCapabilityPolicy(t *testing.T) {
	tests := []struct {
		name         string
		root         bool
		capabilities CapabilityPolicy
		wantValid    bool
	}{
		{
			name:         "application drops all",
			capabilities: CapabilityDropAll,
			wantValid:    true,
		},
		{
			name:         "application cannot retain lifecycle capabilities",
			capabilities: CapabilityRootLifecycle,
		},
		{
			name:         "root lifecycle",
			root:         true,
			capabilities: CapabilityRootLifecycle,
			wantValid:    true,
		},
		{
			name:         "root cannot silently use zero policy",
			root:         true,
			capabilities: "",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			plan := validPlan()
			plan.Command.Root = test.root
			plan.Command.Capabilities = test.capabilities
			err := plan.Validate()
			if test.wantValid && err != nil {
				t.Fatal(err)
			}
			if !test.wantValid && err == nil {
				t.Fatal("inconsistent capability policy was accepted")
			}
		})
	}
}

func TestPlanRejectsReservedEnvironmentOverrides(t *testing.T) {
	for _, name := range []string{"HOME", "TOBY_SANDBOX"} {
		plan := validPlan()
		plan.Environment = append(
			plan.Environment,
			EnvironmentVariable{Name: name, Value: "override"},
		)
		if err := plan.Validate(); err == nil {
			t.Fatalf("reserved environment variable %q was accepted", name)
		}
	}
}

func TestPlanConfinesDedicatedRuntimeAssets(t *testing.T) {
	plan := validPlan()
	plan.RuntimeAssets = []RuntimeAsset{{
		HostPath: "/run/user/1000/toby/run/socket",
		Target:   "/run/toby/sandbox.sock",
		Access:   mount.AccessRegular,
	}}
	if err := plan.Validate(); err != nil {
		t.Fatal(err)
	}

	for _, target := range []string{
		"/run/toby",
		"/run/toby-other/socket",
		"/toby/home/runtime.sock",
	} {
		invalid := plan
		invalid.RuntimeAssets = append([]RuntimeAsset(nil), plan.RuntimeAssets...)
		invalid.RuntimeAssets[0].Target = target
		if err := invalid.Validate(); err == nil {
			t.Fatalf("runtime asset target %q was accepted", target)
		}
	}

	plan.RuntimeAssets = append(plan.RuntimeAssets, RuntimeAsset{
		HostPath: "/run/user/1000/toby/run/nested",
		Target:   "/run/toby/sandbox.sock/nested",
		Access:   mount.AccessRegular,
	})
	if err := plan.Validate(); err == nil {
		t.Fatal("overlapping runtime assets were accepted")
	}
}

func TestPlanAllowsRunPrivateRuntimeAssets(t *testing.T) {
	plan := validPlan()
	runRoot := filepath.Dir(plan.Overlay.Upper)
	plan.RuntimeAssets = []RuntimeAsset{{
		HostPath: filepath.Join(
			runRoot,
			"runtime",
			".assets-0123456789abcdef",
			"asset-000000",
		),
		Target: "/run/toby/npm/sandbox-init.sh",
		Access: mount.AccessReadOnly,
	}}
	if err := plan.Validate(); err != nil {
		t.Fatal(err)
	}

	plan.RuntimeAssets[0].HostPath = filepath.Join(
		runRoot,
		"upper",
		"asset-000000",
	)
	if err := plan.Validate(); err == nil {
		t.Fatal("runtime asset inside the writable root overlay was accepted")
	}
}

func TestPlanKeepsRuntimeAssetBackingDisjointFromRootFS(t *testing.T) {
	plan := validPlan()
	plan.RuntimeAssets = []RuntimeAsset{{
		HostPath: plan.RootFS.Path + "/runtime.sock",
		Target:   "/run/toby/runtime.sock",
		Access:   mount.AccessRegular,
	}}

	if err := plan.Validate(); err == nil {
		t.Fatal("runtime asset backing inside immutable rootfs was accepted")
	}
}

func TestPlanRejectsDuplicateManagedDirectoryKeys(t *testing.T) {
	plan := validPlan()
	duplicate := plan.ManagedDirectories[0]
	duplicate.HostPath = "/data/toby/volumes/duplicate/_data"
	duplicate.Target = "/toby/home/.cache/duplicate"
	plan.ManagedDirectories = append(plan.ManagedDirectories, duplicate)

	if err := plan.Validate(); err == nil {
		t.Fatal("duplicate managed-directory key was accepted")
	}
}

func TestPlanRequiresUniqueRunLocalOverlaySiblings(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*Plan)
	}{
		{
			name: "private home as upper",
			mutate: func(plan *Plan) {
				plan.Overlay.Upper = plan.Home.HostPath
			},
		},
		{
			name: "different parents",
			mutate: func(plan *Plan) {
				plan.Overlay.Work = "/cache/toby/runs/other/work"
			},
		},
		{
			name: "wrong sibling name",
			mutate: func(plan *Plan) {
				plan.Overlay.Upper = "/cache/toby/runs/run-abc123/writable"
			},
		},
		{
			name: "wrong run root",
			mutate: func(plan *Plan) {
				plan.Overlay.Upper = "/cache/toby/runs/other/upper"
				plan.Overlay.Work = "/cache/toby/runs/other/work"
			},
		},
		{
			name: "outside configured run storage",
			mutate: func(plan *Plan) {
				plan.Overlay.Upper = "/arbitrary/run-abc123/upper"
				plan.Overlay.Work = "/arbitrary/run-abc123/work"
			},
		},
		{
			name: "inside rootfs",
			mutate: func(plan *Plan) {
				runRoot := plan.RootFS.Path + "/" + plan.RunID
				plan.Overlay.Upper = runRoot + "/upper"
				plan.Overlay.Work = runRoot + "/work"
			},
		},
		{
			name: "contains persistent home",
			mutate: func(plan *Plan) {
				runRoot := filepath.Dir(plan.Overlay.Upper)
				plan.Home.HostPath = runRoot + "/persistent-home"
			},
		},
		{
			name: "unclean upper",
			mutate: func(plan *Plan) {
				plan.Overlay.Upper = "/cache/toby/runs/run-abc123/unused/../upper"
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			plan := validPlan()
			test.mutate(&plan)
			if err := plan.Validate(); err == nil {
				t.Fatal("unsafe overlay storage was accepted")
			}
		})
	}
}

func TestPlanKeepsImmutableRootFSDisjoint(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*Plan)
	}{
		{
			name: "home inside rootfs",
			mutate: func(plan *Plan) {
				plan.Home.HostPath = plan.RootFS.Path + "/private-home"
				plan.GeneratedFiles[0].HostPath =
					plan.Home.HostPath + "/.config/opencode/opencode.json"
			},
		},
		{
			name: "rootfs inside home",
			mutate: func(plan *Plan) {
				plan.RootFS.Path = plan.Home.HostPath + "/rootfs"
			},
		},
		{
			name: "project inside rootfs",
			mutate: func(plan *Plan) {
				plan.Projects[0].HostPath = plan.RootFS.Path + "/project"
			},
		},
		{
			name: "rootfs inside bind",
			mutate: func(plan *Plan) {
				plan.Binds = []mount.Bind{{
					HostPath: filepath.Dir(plan.RootFS.Path),
					Target:   "/host/images",
					Access:   mount.AccessReadOnly,
				}}
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			plan := validPlan()
			test.mutate(&plan)
			if err := plan.Validate(); err == nil {
				t.Fatal("rootfs host-path alias was accepted")
			}
		})
	}
}

func TestPlanKeepsOwnedBackingsDisjoint(t *testing.T) {
	t.Run("managed inside home", func(t *testing.T) {
		plan := validPlan()
		plan.ManagedDirectories[0].HostPath =
			plan.Home.HostPath + "/managed/opencode"
		if err := plan.Validate(); err == nil {
			t.Fatal("managed-directory backing inside private home was accepted")
		}
	})

	t.Run("home inside managed", func(t *testing.T) {
		plan := validPlan()
		plan.Home.HostPath =
			plan.ManagedDirectories[0].HostPath + "/private-home"
		plan.GeneratedFiles[0].HostPath =
			plan.Home.HostPath + "/.config/opencode/opencode.json"
		if err := plan.Validate(); err == nil {
			t.Fatal("private home inside managed-directory backing was accepted")
		}
	})

	t.Run("home exposed as project", func(t *testing.T) {
		plan := validPlan()
		plan.Projects[0].HostPath = plan.Home.HostPath
		if err := plan.Validate(); err == nil {
			t.Fatal("private home exposed as a project was accepted")
		}
	})
}

func TestPlanRejectsTargetsThatShadowFixedLayout(t *testing.T) {
	tests := []struct {
		name   string
		target string
	}{
		{name: "project", target: "/toby/workspace/app"},
		{name: "workspace child", target: "/toby/workspace/other"},
		{name: "sandbox helper", target: layout.SandboxBinary()},
		{name: "binary directory", target: layout.Bin},
		{name: "runtime", target: "/run/toby/connector.sock"},
		{name: "private home", target: layout.Home},
		{name: "layout root", target: layout.Root},
		{name: "filesystem root", target: "/"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			plan := validPlan()
			plan.Binds = []mount.Bind{{
				HostPath: "/host/value",
				Target:   test.target,
				Access:   mount.AccessReadOnly,
			}}
			if err := plan.Validate(); err == nil {
				t.Fatalf("target %q was accepted", test.target)
			}
		})
	}
}

func TestPlanAllowsExplicitSubmountBeneathPrivateHome(t *testing.T) {
	plan := validPlan()
	plan.Binds = []mount.Bind{{
		HostPath: "/host/docker",
		Target:   "/toby/home/.docker",
		Access:   mount.AccessReadOnly,
	}}
	if err := plan.Validate(); err != nil {
		t.Fatal(err)
	}
}

func TestPlanRejectsConflictingGeneratedFiles(t *testing.T) {
	t.Run("duplicate target", func(t *testing.T) {
		plan := validPlan()
		duplicate := plan.GeneratedFiles[0]
		duplicate.HostPath += ".other"
		plan.GeneratedFiles = append(plan.GeneratedFiles, duplicate)
		if err := plan.Validate(); err == nil {
			t.Fatal("duplicate generated-file target was accepted")
		}
	})

	t.Run("target beneath another file", func(t *testing.T) {
		plan := validPlan()
		plan.GeneratedFiles = append(plan.GeneratedFiles, GeneratedFile{
			HostPath: plan.GeneratedFiles[0].HostPath + "/nested",
			Target:   plan.GeneratedFiles[0].Target + "/nested",
			Data:     []byte("nested"),
			Mode:     0o600,
			UID:      1000,
			GID:      1000,
		})
		if err := plan.Validate(); err == nil {
			t.Fatal("nested generated-file target was accepted")
		}
	})

	t.Run("external bind", func(t *testing.T) {
		plan := validPlan()
		plan.Binds = []mount.Bind{{
			HostPath: "/host/opencode",
			Target:   "/toby/home/.config/opencode",
			Access:   mount.AccessReadOnly,
		}}
		if err := plan.Validate(); err == nil {
			t.Fatal("generated file shadowed by an external bind was accepted")
		}
	})
}

func TestPlanRequiresGeneratedFileToMapNativeBacking(t *testing.T) {
	t.Run("private home", func(t *testing.T) {
		plan := validPlan()
		plan.GeneratedFiles[0].HostPath = "/unrelated/opencode.json"
		if err := plan.Validate(); err == nil {
			t.Fatal("generated file outside its private-home backing was accepted")
		}
	})

	t.Run("managed directory", func(t *testing.T) {
		plan := validPlan()
		plan.GeneratedFiles = []GeneratedFile{{
			HostPath: "/data/toby/volumes/def456/_data/config.json",
			Target:   "/toby/home/.local/share/opencode/config.json",
			Data:     []byte("managed"),
			Mode:     0o600,
			UID:      1000,
			GID:      1000,
		}}
		if err := plan.Validate(); err != nil {
			t.Fatal(err)
		}
	})
}

func TestPlanRequiresGeneratedFileHostIdentity(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*GeneratedFile)
	}{
		{
			name: "uid",
			mutate: func(file *GeneratedFile) {
				file.UID++
			},
		},
		{
			name: "gid",
			mutate: func(file *GeneratedFile) {
				file.GID++
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			plan := validPlan()
			test.mutate(&plan.GeneratedFiles[0])

			if err := plan.Validate(); err == nil {
				t.Fatal("generated file with a foreign host identity was accepted")
			}
		})
	}
}

func TestPlanRequiresGeneratedFileOwnerAccess(t *testing.T) {
	plan := validPlan()
	plan.GeneratedFiles[0].Mode = 0o044

	if err := plan.Validate(); err == nil {
		t.Fatal("generated file without owner read or write access was accepted")
	}
}

func TestPlanAcceptsProjectNames(t *testing.T) {
	for _, name := range []string{"_temp", "project name", "日本語"} {
		t.Run(name, func(t *testing.T) {
			plan := validPlan()
			plan.Projects[0].Name = name
			plan.Projects[0].Target = path.Join(layout.Workspace, name)

			if err := plan.Validate(); err != nil {
				t.Fatalf("project name %q was rejected: %v", name, err)
			}
		})
	}
}

func validPlan() Plan {
	return Plan{
		RunID: "run-abc123",
		RootFS: RootFS{
			Digest: "sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef",
			Path:   "/data/toby/images/objects/abcdef/sha256/0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef/bundle/rootfs",
		},
		Overlay: Overlay{
			RunStorageDir: "/cache/toby/runs",
			Upper:         "/cache/toby/runs/run-abc123/upper",
			Work:          "/cache/toby/runs/run-abc123/work",
		},
		Home: Home{ID: "abc123", HostPath: "/data/toby/volumes/abc123/_data"},
		Projects: []Project{
			{Name: "app", HostPath: "/projects/app", Target: "/toby/workspace/app"},
		},
		ManagedDirectories: []mount.Entry{
			{
				Key:      mount.Key{Type: mount.TypeTool, Name: "opencode", Purpose: "state"},
				Profile:  "default",
				HostPath: "/data/toby/volumes/def456/_data",
				Target:   "/toby/home/.local/share/opencode",
				Access:   mount.AccessRegular,
			},
		},
		GeneratedFiles: []GeneratedFile{
			{HostPath: "/data/toby/volumes/abc123/_data/.config/opencode/opencode.json", Target: "/toby/home/.config/opencode/opencode.json", Data: []byte("data"), Mode: 0o600, UID: 1000, GID: 1000},
		},
		SandboxBinary: Binary{HostPath: "/proc/self/exe", Target: layout.SandboxBinary()},
		Workdir:       "/toby/workspace/app",
		Environment:   []EnvironmentVariable{{Name: "PATH", Value: "/usr/bin:/bin"}},
		Identity:      Identity{HostUID: 1000, HostGID: 1000},
		Namespaces:    Namespaces{Network: NetworkHost},
		Command: Command{
			Argv:         []string{"sh"},
			Mode:         ExecutionNonInteractive,
			Capabilities: CapabilityDropAll,
		},
	}
}
