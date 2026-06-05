package sandbox

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"petris.dev/toby/control"
	"petris.dev/toby/control/methods/env"
	"petris.dev/toby/control/methods/files"
)

func TestSandboxManagerHandlesFileCommands(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "context", "file.txt")
	s := NewRuntime(fileRouter(t), nil, nil)
	request := mustRequest(t, 1, files.MethodCreate, files.CreateParams{Path: path, Data: []byte("hello"), Mode: 0o600})
	response, err := s.Handle(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	mustOK(t, response)
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "hello" {
		t.Fatalf("file data = %q", data)
	}

	request = mustRequest(t, 2, files.MethodDelete, files.DeleteParams{Path: filepath.Join(dir, "context"), Recursive: true})
	response, err = s.Handle(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	mustOK(t, response)
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("file still exists: %v", err)
	}
}

func TestSandboxManagerFileCommandsDefaultModes(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "context", "nested", "file.txt")
	s := NewRuntime(fileRouter(t), nil, nil)
	request := mustRequest(t, 1, files.MethodCreate, files.CreateParams{Path: path, Data: []byte("hello")})
	response, err := s.Handle(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	mustOK(t, response)
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != 0o644 {
		t.Fatalf("file mode = %#o, want 0644", got)
	}
	parent, err := os.Stat(filepath.Dir(path))
	if err != nil {
		t.Fatal(err)
	}
	if got := parent.Mode().Perm(); got != 0o755 {
		t.Fatalf("parent mode = %#o, want 0755", got)
	}

	dirPath := filepath.Join(dir, "created-dir")
	request = mustRequest(t, 2, files.MethodMkdir, files.MkdirParams{Path: dirPath})
	response, err = s.Handle(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	mustOK(t, response)
	info, err = os.Stat(dirPath)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != 0o755 {
		t.Fatalf("dir mode = %#o, want 0755", got)
	}
}

func TestEnvironmentServiceGetSetAndUnset(t *testing.T) {
	environment := env.New()
	router, err := control.NewRouter([]control.Capability{environment})
	if err != nil {
		t.Fatal(err)
	}
	runtime := NewRuntime(router, environment, nil)
	request := mustRequest(t, 1, env.MethodSet, env.SetParams{Name: "TOBY_TEST_SERVICE_ENV", Value: "value"})
	response, err := runtime.Handle(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	mustOK(t, response)

	request = mustRequest(t, 2, env.MethodGet, nil)
	response, err = runtime.Handle(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := control.DecodeResponse(response)
	if err != nil {
		t.Fatal(err)
	}
	result, err := env.DecodeGetResult(decoded.Result)
	if err != nil {
		t.Fatal(err)
	}
	if result.Environment["TOBY_TEST_SERVICE_ENV"] != "value" {
		t.Fatalf("environment = %#v", result.Environment)
	}

	request = mustRequest(t, 3, env.MethodSet, env.SetParams{Name: "TOBY_TEST_SERVICE_ENV", Value: ""})
	response, err = runtime.Handle(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	mustOK(t, response)
	if got, ok := environment.Get("TOBY_TEST_SERVICE_ENV"); ok {
		t.Fatalf("unset env = %q, %v", got, ok)
	}
}

func fileRouter(t *testing.T) *control.Router {
	t.Helper()
	router, err := control.NewRouter([]control.Capability{files.New()})
	if err != nil {
		t.Fatal(err)
	}
	return router
}

// mustRequest builds a JSON-RPC request envelope for a control method, marshaling
// params when present.
func mustRequest(t *testing.T, id int64, method string, params any) []byte {
	t.Helper()
	var data []byte
	if params != nil {
		encoded, err := json.Marshal(params)
		if err != nil {
			t.Fatal(err)
		}
		data = encoded
	}
	request, err := control.NewRequest(id, method, data)
	if err != nil {
		t.Fatal(err)
	}
	return request
}

func mustOK(t *testing.T, response []byte) {
	t.Helper()
	decoded, err := control.DecodeResponse(response)
	if err != nil {
		t.Fatal(err)
	}
	if decoded.Error != nil {
		t.Fatalf("response error = %#v", decoded.Error)
	}
}
