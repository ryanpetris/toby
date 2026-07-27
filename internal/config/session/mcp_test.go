package sessionconfig

// Tests MCP transport validation and Holder deep-copy boundaries.

import (
	"reflect"
	"strings"
	"testing"
)

func TestMCPServerValidate(t *testing.T) {
	tests := []struct {
		name    string
		server  MCPServer
		wantErr string
	}{
		{
			name: "stdio",
			server: MCPServer{
				Name:      "docs",
				Transport: MCPTransportStdio,
				Command:   "/toby/bin/tobys",
				Args:      []string{"resource", "connect", "--", "docs"},
			},
		},
		{
			name: "http",
			server: MCPServer{
				Name:      "docs",
				Transport: MCPTransportHTTP,
				URL:       "http://127.0.0.1/capability",
			},
		},
		{
			name:    "missing transport",
			server:  MCPServer{Name: "docs", URL: "http://127.0.0.1"},
			wantErr: "unsupported transport",
		},
		{
			name: "ambiguous stdio",
			server: MCPServer{
				Name:      "docs",
				Transport: MCPTransportStdio,
				Command:   "/toby/bin/tobys",
				URL:       "http://127.0.0.1",
			},
			wantErr: "must not have a URL",
		},
		{
			name: "ambiguous http",
			server: MCPServer{
				Name:      "docs",
				Transport: MCPTransportHTTP,
				Command:   "/toby/bin/tobys",
				URL:       "http://127.0.0.1",
			},
			wantErr: "must not have a command",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := test.server.Validate()
			if test.wantErr == "" {
				if err != nil {
					t.Fatal(err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), test.wantErr) {
				t.Fatalf("Validate error = %v, want containing %q", err, test.wantErr)
			}
		})
	}
}

func TestHolderCopiesMutableConfiguration(t *testing.T) {
	original := Config{
		MCPServers: []MCPServer{{
			Name:      "docs",
			Transport: MCPTransportStdio,
			Command:   "/toby/bin/tobys",
			Args:      []string{"resource", "connect", "--", "docs"},
		}},
		Models: []ModelsEndpoint{{
			ID:         "provider",
			Credential: "synthetic-credential",
			Models:     map[string]any{"nested": map[string]any{"name": "model"}},
		}},
		Projects:    []string{"/toby/workspace/project"},
		Permissions: map[string]string{"/project": "allow"},
		Instructions: Instructions{
			Paths:    []string{"/instructions"},
			Contents: [][]byte{[]byte("rules")},
		},
	}

	holder := NewHolder()
	holder.Set(original)
	original.MCPServers[0].Args[0] = "changed"
	original.Models[0].Credential = "changed"
	original.Models[0].Models["nested"].(map[string]any)["name"] = "changed"
	original.Projects[0] = "/changed"
	original.Permissions["/project"] = "deny"
	original.Instructions.Paths[0] = "/changed"
	original.Instructions.Contents[0][0] = 'X'

	got := holder.Snapshot()
	want := Config{
		MCPServers: []MCPServer{{
			Name:      "docs",
			Transport: MCPTransportStdio,
			Command:   "/toby/bin/tobys",
			Args:      []string{"resource", "connect", "--", "docs"},
		}},
		Models: []ModelsEndpoint{{
			ID:         "provider",
			Credential: "synthetic-credential",
			Models:     map[string]any{"nested": map[string]any{"name": "model"}},
		}},
		Projects:    []string{"/toby/workspace/project"},
		Permissions: map[string]string{"/project": "allow"},
		Instructions: Instructions{
			Paths:    []string{"/instructions"},
			Contents: [][]byte{[]byte("rules")},
		},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Get = %#v, want %#v", got, want)
	}

	got.MCPServers[0].Args[0] = "mutated"
	if next := holder.Snapshot(); next.MCPServers[0].Args[0] != "resource" {
		t.Fatalf("Snapshot returned shared state: %#v", next.MCPServers)
	}
}

func TestHolderConfigRequiresResolution(t *testing.T) {
	holder := NewHolder()
	if _, err := holder.Config(); err == nil {
		t.Fatal("unresolved holder returned a configuration")
	}

	holder.Set(Config{})
	if _, err := holder.Config(); err != nil {
		t.Fatalf("resolved empty configuration: %v", err)
	}
}
