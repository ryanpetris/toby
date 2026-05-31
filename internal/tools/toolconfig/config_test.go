package toolconfig

import (
	"reflect"
	"testing"
)

func TestCommandParts(t *testing.T) {
	tests := []struct {
		name        string
		raw         any
		wantCommand string
		wantArgs    []string
		wantErr     string
	}{
		{name: "string", raw: "npx", wantCommand: "npx"},
		{name: "array", raw: []any{"npx", "-y", "docs-mcp"}, wantCommand: "npx", wantArgs: []string{"-y", "docs-mcp"}},
		{name: "empty string", raw: "", wantErr: `mcp server "empty string" command is empty`},
		{name: "empty array", raw: []any{}, wantErr: `mcp server "empty array" command is empty`},
		{name: "bad first", raw: []any{1}, wantErr: `mcp server "bad first" command must start with a string`},
		{name: "bad arg", raw: []any{"npx", 1}, wantErr: `mcp server "bad arg" command arguments must be strings`},
		{name: "missing", raw: nil, wantErr: `mcp server "missing" command is required`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			command, args, err := CommandParts(tt.name, tt.raw)
			if tt.wantErr != "" {
				if err == nil || err.Error() != tt.wantErr {
					t.Fatalf("err = %v, want %q", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if command != tt.wantCommand || !reflect.DeepEqual(args, tt.wantArgs) {
				t.Fatalf("parts = %q, %#v; want %q, %#v", command, args, tt.wantCommand, tt.wantArgs)
			}
		})
	}
}

func TestCopyEnvVarsAndHeaders(t *testing.T) {
	dst := map[string]any{
		"env":     map[string]any{"EXISTING": "keep"},
		"headers": map[string]any{"Authorization": "Bearer literal"},
	}
	src := map[string]any{
		"env_vars":             []any{"EXISTING", "TOKEN", ""},
		"env_http_headers":     map[string]any{"X-Token": "TOKEN", "X-Blank": ""},
		"bearer_token_env_var": "BEARER_TOKEN",
	}
	CopyEnvVars(dst, src)
	CopyEnvHeaders(dst, src)
	CopyBearerTokenEnv(dst, src)

	env := dst["env"].(map[string]any)
	if env["EXISTING"] != "keep" || env["TOKEN"] != "${TOKEN}" {
		t.Fatalf("env = %#v", env)
	}
	headers := dst["headers"].(map[string]any)
	if headers["Authorization"] != "Bearer literal" || headers["X-Token"] != "${TOKEN}" {
		t.Fatalf("headers = %#v", headers)
	}
	if _, ok := headers["X-Blank"]; ok {
		t.Fatalf("blank header copied: %#v", headers)
	}
}

func TestJoinInstructions(t *testing.T) {
	instructions := [][]byte{[]byte("\n"), []byte("# one\n"), []byte("# two\n\n")}
	if got := string(JoinInstructions(instructions)); got != "# one\n\n# two\n" {
		t.Fatalf("joined = %q", got)
	}
	if got := JoinInstructionsString([][]byte{[]byte("  ")}); got != "" {
		t.Fatalf("empty string join = %q", got)
	}
	if got := string(JoinInstructionsOrNewline(nil)); got != "\n" {
		t.Fatalf("empty newline join = %q", got)
	}
}
