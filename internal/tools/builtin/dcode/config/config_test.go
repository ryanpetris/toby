package config

// Tests for Deep Agents Code synthetic MCP and instruction files.

import (
	"encoding/json"
	"testing"

	"petris.dev/toby/config/session"
	contextfiles "petris.dev/toby/context/files"
)

func TestRegisterContextFilesWritesMCPConfig(t *testing.T) {
	registrar := &memoryRegistrar{}
	cfg := sessionconfig.Config{
		MCPServers: []sessionconfig.MCPServer{
			{Name: "docs", URL: "http://127.0.0.1:12345/proxy/docs"},
			{Name: "toby", URL: "http://127.0.0.1:12345/proxy/toby"},
		},
	}

	if err := RegisterContextFiles(registrar, cfg); err != nil {
		t.Fatal(err)
	}

	files := registrar.Files()
	mcp := decodeMCP(t, fileByPath(t, files, MCPConfigPath).Data)
	servers := mcp["mcpServers"].(map[string]any)
	if servers["docs"].(map[string]any)["url"] != "http://127.0.0.1:12345/proxy/docs" {
		t.Fatalf("mcp servers = %#v", servers)
	}
}

func TestInstructionsJoinsContents(t *testing.T) {
	cfg := sessionconfig.Config{Instructions: sessionconfig.Instructions{Contents: [][]byte{[]byte("# Toby instructions\n")}}}
	if got := string(Instructions(cfg)); got != "# Toby instructions\n" {
		t.Fatalf("instructions = %q", got)
	}
}

type memoryRegistrar struct {
	files []contextfiles.File
}

func (r *memoryRegistrar) AddBytes(path string, data []byte, mode uint32) error {
	r.files = append(r.files, contextfiles.File{Path: path, Data: append([]byte(nil), data...), Mode: mode})
	return nil
}

func (r *memoryRegistrar) Files() []contextfiles.File {
	return append([]contextfiles.File(nil), r.files...)
}

func decodeMCP(t *testing.T, data []byte) map[string]any {
	t.Helper()
	var out map[string]any
	if err := json.Unmarshal(data, &out); err != nil {
		t.Fatal(err)
	}
	return out
}

func fileByPath(t *testing.T, files []contextfiles.File, path string) contextfiles.File {
	t.Helper()
	for _, file := range files {
		if file.Path == path {
			return file
		}
	}
	t.Fatalf("missing file %s in %#v", path, files)
	return contextfiles.File{}
}
