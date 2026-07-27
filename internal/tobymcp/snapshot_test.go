package tobymcp

// Exercises strict snapshot decoding, validation, deep cloning, and the
// structural exclusion of secret-bearing transport fields.

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestDecodeSessionSnapshotAcceptsCompleteSafeView(t *testing.T) {
	t.Parallel()

	snapshot, err := DecodeSessionSnapshot(json.RawMessage(validSessionSnapshotFixture))
	if err != nil {
		t.Fatal(err)
	}

	if !snapshot.Debug ||
		snapshot.Runtime.Profile != "default" ||
		snapshot.Runtime.Runtime != "bubblewrap" ||
		snapshot.Tools.Primary != "codex" ||
		len(snapshot.Projects) != 1 ||
		len(snapshot.Mounts) != 1 ||
		len(snapshot.Binds) != 1 ||
		len(snapshot.Models) != 1 ||
		len(snapshot.MCPs) != 2 {
		t.Fatalf("decoded snapshot = %#v", snapshot)
	}

	encoded, err := json.Marshal(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{
		`"url"`,
		`"headers"`,
		`"command"`,
		`"argv"`,
		`"environment"`,
		`"credentials"`,
		`"hostPath"`,
		`"source"`,
	} {
		if strings.Contains(string(encoded), forbidden) {
			t.Fatalf(
				"encoded snapshot contains forbidden field %s: %s",
				forbidden,
				encoded,
			)
		}
	}
}

func TestDecodeSessionSnapshotRejectsSecretBearingAndAmbiguousFields(
	t *testing.T,
) {
	t.Parallel()

	prefix := invalidSessionPrefixFixture
	tests := []struct {
		name string
		data string
		want string
	}{
		{
			name: "root URL",
			data: prefix + `,"url":"https://secret.example"}`,
			want: "unknown field",
		},
		{
			name: "models headers",
			data: prefix + secretModelsFixture,
			want: "unknown field",
		},
		{
			name: "MCP command",
			data: prefix + secretMCPCommandFixture,
			want: "unknown field",
		},
		{
			name: "MCP diagnostic status",
			data: prefix + secretMCPStatusFixture,
			want: "status is invalid",
		},
		{
			name: "bind source",
			data: prefix + secretBindSourceFixture,
			want: "unknown field",
		},
		{
			name: "duplicate field",
			data: prefix + `,"debug":true}`,
			want: "duplicate object key",
		},
		{
			name: "trailing value",
			data: prefix + `} true`,
			want: "trailing",
		},
	}

	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			_, err := DecodeSessionSnapshot(
				json.RawMessage(test.data),
			)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf(
					"DecodeSessionSnapshot() error = %v, want containing %q",
					err,
					test.want,
				)
			}
		})
	}
}

func TestSessionSnapshotCloneDetachesMutableCollections(t *testing.T) {
	t.Parallel()

	snapshot := validSessionSnapshot()
	clone := snapshot.Clone()

	snapshot.Tools.Active[0] = "changed"
	snapshot.Tools.Available[0].ContextGroups[0] = "changed"
	snapshot.Tools.Groups["codex"] = "changed"
	snapshot.Models[0].Models[0] = "changed"

	if got := clone.Tools.Active[0]; got != "codex" {
		t.Fatalf("cloned active tool = %q, want codex", got)
	}
	if got := clone.Tools.Available[0].ContextGroups[0]; got != "agents" {
		t.Fatalf("cloned context group = %q, want agents", got)
	}
	if got := clone.Tools.Groups["codex"]; got != "ai" {
		t.Fatalf("cloned tool group = %q, want ai", got)
	}
	if got := clone.Models[0].Models[0]; got != "gpt-5" {
		t.Fatalf("cloned models endpoint model = %q, want gpt-5", got)
	}
}

func validSessionSnapshot() SessionSnapshot {
	return SessionSnapshot{
		Debug: true,
		Runtime: SessionRuntime{
			Name:      "work",
			Profile:   "default",
			Runtime:   "bubblewrap",
			Network:   "host",
			Home:      "/toby/home",
			Workspace: "/toby/workspace",
			Root:      "/toby",
			Bin:       "/toby/bin",
			Workdir:   "/toby/workspace",
		},
		Tools: SessionTools{
			Primary: "codex",
			Active:  []string{"codex"},
			Available: []SessionTool{{
				Name:          "codex",
				Launchable:    true,
				ContextGroups: []string{"agents"},
			}},
			Groups: map[string]string{"codex": "ai"},
		},
		Models: []SessionModelsEndpoint{{
			Name:   "models",
			Type:   "openai",
			Models: []string{"gpt-5"},
		}},
	}
}
