package resource

// Verifies deterministic identities, HMAC separation, redaction, and scope
// narrowing.

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestBuilderCanonicalizesOrder(t *testing.T) {
	builder := testBuilder(t, "a")
	first := testSpec()
	first.Environment = []EnvironmentVariable{
		{Name: "TOKEN", Value: "secret", Sensitive: true},
		{Name: "MODE", Value: "read"},
	}
	first.Mounts = []Mount{
		{Source: "/data/b", Target: "/sandbox/b", Access: "read_only", Scope: ScopeUser},
		{Source: "/data/a", Target: "/sandbox/a", Access: "regular", Scope: ScopeHome},
	}
	first.ScopeIdentity = "home-a"

	second := first
	second.Environment = []EnvironmentVariable{first.Environment[1], first.Environment[0]}
	second.Mounts = []Mount{first.Mounts[1], first.Mounts[0]}

	firstKey, err := builder.Build(first)
	if err != nil {
		t.Fatal(err)
	}
	secondKey, err := builder.Build(second)
	if err != nil {
		t.Fatal(err)
	}
	if firstKey != secondKey {
		t.Fatalf("reordered equivalent specs differ: %s != %s", firstKey, secondKey)
	}
}

func TestBuilderSeparatesSecretsAndAgentKeys(t *testing.T) {
	spec := testSpec()
	spec.Environment = []EnvironmentVariable{{Name: "TOKEN", Value: "alpha", Sensitive: true}}

	firstBuilder := testBuilder(t, "a")
	first, err := firstBuilder.Build(spec)
	if err != nil {
		t.Fatal(err)
	}
	spec.Environment[0].Value = "beta"
	second, err := firstBuilder.Build(spec)
	if err != nil {
		t.Fatal(err)
	}
	if first == second {
		t.Fatal("different secret values produced the same key")
	}

	spec.Environment[0].Value = "alpha"
	third, err := testBuilder(t, "b").Build(spec)
	if err != nil {
		t.Fatal(err)
	}
	if first == third {
		t.Fatal("different agent HMAC keys produced the same key")
	}

	summary, err := json.Marshal(first.Summary())
	if err != nil {
		t.Fatal(err)
	}
	for _, protected := range []string{"alpha", "TOKEN"} {
		if strings.Contains(string(summary), protected) || strings.Contains(first.String(), protected) {
			t.Fatalf("safe representation leaked %q", protected)
		}
	}
}

func TestBuilderChangesEveryIdentityDimension(t *testing.T) {
	builder := testBuilder(t, "a")
	base := testSpec()
	baseKey, err := builder.Build(base)
	if err != nil {
		t.Fatal(err)
	}

	tests := map[string]func(*Spec){
		"kind":        func(s *Spec) { s.Kind = KindCaddy },
		"transport":   func(s *Spec) { s.Transport = "https" },
		"manifest":    func(s *Spec) { s.ManifestDigest = "sha256:bb" },
		"rootfs":      func(s *Spec) { s.RootFSDigest = "sha256:cc" },
		"argv":        func(s *Spec) { s.Argv = append(s.Argv, "--other") },
		"workdir":     func(s *Spec) { s.Workdir = "/other" },
		"uid":         func(s *Spec) { s.Identity.UID++ },
		"gid":         func(s *Spec) { s.Identity.GID++ },
		"environment": func(s *Spec) { s.Environment = []EnvironmentVariable{{Name: "MODE", Value: "x"}} },
		"endpoint path": func(s *Spec) {
			s.Endpoint.Path = "/other"
		},
		"endpoint socket": func(s *Spec) {
			s.Endpoint.Kind = EndpointUnix
			s.Endpoint.Port = 0
			s.Endpoint.Socket = "/run/toby/other.sock"
		},
		"mount": func(s *Spec) {
			s.Mounts = []Mount{{Source: "/data", Target: "/data", Access: "read_only", Scope: ScopeUser}}
		},
		"network":          func(s *Spec) { s.Network = NetworkPrivate },
		"idle timeout":     func(s *Spec) { s.IdleTimeout = time.Minute },
		"bridge version":   func(s *Spec) { s.BridgeVersion = "2" },
		"protocol version": func(s *Spec) { s.ProtocolVersion = "2" },
		"scope": func(s *Spec) {
			s.RequestedScope = ScopeHome
			s.ScopeIdentity = "home-a"
		},
		"run authority": func(s *Spec) {
			s.RunAuthority = RunAuthorityPresent
			s.ScopeIdentity = "run-a"
		},
	}

	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			changed := base
			changed.Argv = append([]string(nil), base.Argv...)
			mutate(&changed)
			key, err := builder.Build(changed)
			if name == "transport" {
				if err == nil {
					t.Fatal("invalid transport was accepted")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if key == baseKey {
				t.Fatalf("%s did not change resource identity", name)
			}
		})
	}
}

func TestBuilderSeparatesPinnedMountSourceIdentity(t *testing.T) {
	builder := testBuilder(t, "a")
	first := testSpec()
	first.Mounts = []Mount{{
		Source: "/data",
		SourceIdentity: MountSourceIdentity{
			Device:   1,
			Inode:    2,
			FileType: 0o040000,
		},
		Target: "/data",
		Access: "read_only",
		Scope:  ScopeUser,
	}}
	second := first
	second.Mounts = append([]Mount(nil), first.Mounts...)
	second.Mounts[0].SourceIdentity.Inode++

	firstKey, err := builder.Build(first)
	if err != nil {
		t.Fatal(err)
	}
	secondKey, err := builder.Build(second)
	if err != nil {
		t.Fatal(err)
	}
	if firstKey == secondKey {
		t.Fatal("different pinned mount inodes produced the same key")
	}
}

func TestBuilderRejectsNegativeIdleTimeout(t *testing.T) {
	spec := testSpec()
	spec.IdleTimeout = -time.Second

	if _, err := testBuilder(t, "a").Build(spec); err == nil {
		t.Fatal("Build accepted a negative idle timeout")
	}
}

func TestEffectiveScopeOnlyNarrows(t *testing.T) {
	tests := []struct {
		name         string
		requested    Scope
		mounts       []Mount
		runAuthority RunAuthority
		want         Scope
	}{
		{name: "user", requested: ScopeUser, runAuthority: RunAuthorityAbsent, want: ScopeUser},
		{name: "home mount", requested: ScopeUser, mounts: []Mount{{Scope: ScopeHome}}, runAuthority: RunAuthorityAbsent, want: ScopeHome},
		{name: "project mount", requested: ScopeHome, mounts: []Mount{{Scope: ScopeProject}}, runAuthority: RunAuthorityAbsent, want: ScopeProject},
		{name: "already narrower", requested: ScopeRun, mounts: []Mount{{Scope: ScopeHome}}, runAuthority: RunAuthorityAbsent, want: ScopeRun},
		{name: "run authority", requested: ScopeUser, runAuthority: RunAuthorityPresent, want: ScopeRun},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := EffectiveScope(test.requested, test.mounts, test.runAuthority)
			if err != nil {
				t.Fatal(err)
			}
			if got != test.want {
				t.Fatalf("EffectiveScope = %q, want %q", got, test.want)
			}
		})
	}
}

func TestEffectiveScopeRequiresRunAuthorityDeclaration(t *testing.T) {
	if _, err := EffectiveScope(ScopeUser, nil, ""); err == nil {
		t.Fatal("EffectiveScope accepted an omitted run authority declaration")
	}
}

func TestBuilderDerivesEffectiveScope(t *testing.T) {
	builder := testBuilder(t, "a")

	spec := testSpec()
	spec.Mounts = []Mount{{
		Source: "/home/source",
		Target: "/home/target",
		Access: "regular",
		Scope:  ScopeHome,
	}}
	spec.ScopeIdentity = "home-a"

	key, err := builder.Build(spec)
	if err != nil {
		t.Fatal(err)
	}
	if key.Summary().Scope != ScopeHome {
		t.Fatalf("Key scope = %q, want %q", key.Summary().Scope, ScopeHome)
	}
}

func TestBuilderIncludesRunAuthorityInIdentity(t *testing.T) {
	builder := testBuilder(t, "a")

	absent := testSpec()
	absent.RequestedScope = ScopeRun
	absent.ScopeIdentity = "run-a"

	present := absent
	present.RunAuthority = RunAuthorityPresent

	absentKey, err := builder.Build(absent)
	if err != nil {
		t.Fatal(err)
	}
	presentKey, err := builder.Build(present)
	if err != nil {
		t.Fatal(err)
	}
	if absentKey == presentKey {
		t.Fatal("run authority declaration did not change resource identity")
	}
}

func TestBuilderRequiresRunAuthorityDeclaration(t *testing.T) {
	builder := testBuilder(t, "a")
	spec := testSpec()
	spec.RunAuthority = ""

	if _, err := builder.Build(spec); err == nil {
		t.Fatal("Build accepted an omitted run authority declaration")
	}
}

func TestBuilderValidatesIdentityAgainstEffectiveScope(t *testing.T) {
	builder := testBuilder(t, "a")

	spec := testSpec()
	spec.Mounts = []Mount{{
		Source: "/project/source",
		Target: "/project/target",
		Access: "read_only",
		Scope:  ScopeProject,
	}}

	if _, err := builder.Build(spec); err == nil {
		t.Fatal("Build accepted project-scoped mounts without a project identity")
	}

	spec = testSpec()
	spec.RunAuthority = RunAuthorityPresent
	if _, err := builder.Build(spec); err == nil {
		t.Fatal("Build accepted run authority without a run identity")
	}
}

func TestBuilderRejectsInvalidUTF8CollisionInputs(t *testing.T) {
	builder := testBuilder(t, "a")

	for _, invalid := range []string{string([]byte{0xfe}), string([]byte{0xff})} {
		spec := testSpec()
		spec.Argv = append(spec.Argv, invalid)

		if _, err := builder.Build(spec); err == nil {
			t.Fatalf("Build accepted collision-prone argv bytes %x", []byte(invalid))
		}
	}
}

func TestBuilderRejectsInvalidUTF8InEveryStringField(t *testing.T) {
	invalid := string([]byte{0xff})
	tests := map[string]func(*Spec){
		"kind":            func(s *Spec) { s.Kind = Kind(invalid) },
		"transport":       func(s *Spec) { s.Transport = Transport(invalid) },
		"manifest digest": func(s *Spec) { s.ManifestDigest = invalid },
		"rootfs digest":   func(s *Spec) { s.RootFSDigest = invalid },
		"argv":            func(s *Spec) { s.Argv[0] = invalid },
		"workdir":         func(s *Spec) { s.Workdir = invalid },
		"environment name": func(s *Spec) {
			s.Environment = []EnvironmentVariable{{Name: invalid, Value: "value"}}
		},
		"environment value": func(s *Spec) {
			s.Environment = []EnvironmentVariable{{Name: "VALUE", Value: invalid}}
		},
		"endpoint kind":   func(s *Spec) { s.Endpoint.Kind = EndpointKind(invalid) },
		"endpoint socket": func(s *Spec) { s.Endpoint.Socket = invalid },
		"endpoint path":   func(s *Spec) { s.Endpoint.Path = invalid },
		"mount source": func(s *Spec) {
			s.Mounts = []Mount{{Source: invalid, Target: "/target", Access: "read_only", Scope: ScopeUser}}
		},
		"mount target": func(s *Spec) {
			s.Mounts = []Mount{{Source: "/source", Target: invalid, Access: "read_only", Scope: ScopeUser}}
		},
		"mount access": func(s *Spec) {
			s.Mounts = []Mount{{Source: "/source", Target: "/target", Access: invalid, Scope: ScopeUser}}
		},
		"mount scope": func(s *Spec) {
			s.Mounts = []Mount{{Source: "/source", Target: "/target", Access: "read_only", Scope: Scope(invalid)}}
		},
		"network":          func(s *Spec) { s.Network = Network(invalid) },
		"bridge version":   func(s *Spec) { s.BridgeVersion = invalid },
		"protocol version": func(s *Spec) { s.ProtocolVersion = invalid },
		"requested scope":  func(s *Spec) { s.RequestedScope = Scope(invalid) },
		"run authority":    func(s *Spec) { s.RunAuthority = RunAuthority(invalid) },
		"scope identity":   func(s *Spec) { s.RequestedScope, s.ScopeIdentity = ScopeHome, invalid },
	}

	builder := testBuilder(t, "a")
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			spec := testSpec()
			mutate(&spec)

			_, err := builder.Build(spec)
			if err == nil {
				t.Fatal("Build accepted invalid UTF-8")
			}
			if !strings.Contains(err.Error(), "invalid UTF-8") {
				t.Fatalf("Build error = %q, want invalid UTF-8 rejection", err)
			}
		})
	}
}

func TestBuilderDoesNotMutateInput(t *testing.T) {
	builder := testBuilder(t, "a")
	spec := testSpec()
	spec.Environment = []EnvironmentVariable{{Name: "Z", Value: "z"}, {Name: "A", Value: "a"}}
	spec.Mounts = []Mount{
		{Source: "/z", Target: "/z", Access: "read_only", Scope: ScopeUser},
		{Source: "/a", Target: "/a", Access: "read_only", Scope: ScopeUser},
	}
	before := spec
	before.Argv = append([]string(nil), spec.Argv...)
	before.Environment = append([]EnvironmentVariable(nil), spec.Environment...)
	before.Mounts = append([]Mount(nil), spec.Mounts...)

	if _, err := builder.Build(spec); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(spec, before) {
		t.Fatalf("Build mutated input:\n got %#v\nwant %#v", spec, before)
	}
}

func testBuilder(t *testing.T, fill string) *Builder {
	t.Helper()
	builder, err := NewBuilder([]byte(strings.Repeat(fill, minimumHMACKeyBytes)))
	if err != nil {
		t.Fatal(err)
	}
	return builder
}

func testSpec() Spec {
	return Spec{
		Kind:            KindMCPHTTP,
		Transport:       TransportHTTP,
		ManifestDigest:  "sha256:aa",
		RootFSDigest:    "sha256:aa",
		Argv:            []string{"mcp-server", "--port", "3000"},
		Workdir:         "/",
		Identity:        Identity{UID: 1000, GID: 1000},
		Endpoint:        Endpoint{Kind: EndpointTCP, Port: 3000, Path: "/mcp"},
		Network:         NetworkHost,
		BridgeVersion:   "1",
		ProtocolVersion: "2025-06-18",
		RequestedScope:  ScopeUser,
		RunAuthority:    RunAuthorityAbsent,
	}
}
