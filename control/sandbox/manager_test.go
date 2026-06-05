package sandbox

import (
	"context"
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
	request, err := files.NewCreateRequest(1, files.CreateParams{Path: path, Data: []byte("hello"), Mode: 0o600})
	if err != nil {
		t.Fatal(err)
	}
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

	request, err = files.NewDeleteRequest(2, files.DeleteParams{Path: filepath.Join(dir, "context"), Recursive: true})
	if err != nil {
		t.Fatal(err)
	}
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
	request, err := files.NewCreateRequest(1, files.CreateParams{Path: path, Data: []byte("hello")})
	if err != nil {
		t.Fatal(err)
	}
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
	request, err = files.NewMkdirRequest(2, files.MkdirParams{Path: dirPath})
	if err != nil {
		t.Fatal(err)
	}
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

func TestRuntimeEnvironmentInitializesFromProcessEnv(t *testing.T) {
	t.Setenv("TOBY_TEST_STARTUP_ENV", "startup")
	runtime := NewRuntime(nil, nil, nil)
	if got, ok := runtime.getEnvironment("TOBY_TEST_STARTUP_ENV"); !ok || got != "startup" {
		t.Fatalf("startup env = %q, %v", got, ok)
	}

	runtime.setEnvironment("TOBY_TEST_STARTUP_ENV", "updated")
	if got, ok := runtime.getEnvironment("TOBY_TEST_STARTUP_ENV"); !ok || got != "updated" {
		t.Fatalf("updated env = %q, %v", got, ok)
	}
	runtime.setEnvironment("TOBY_TEST_STARTUP_ENV", "")
	if got, ok := runtime.getEnvironment("TOBY_TEST_STARTUP_ENV"); ok {
		t.Fatalf("unset env = %q, %v", got, ok)
	}
}

func TestEnvironmentServiceGetSetAndUnset(t *testing.T) {
	environment := env.New()
	router, err := control.NewRouter([]control.Capability{environment})
	if err != nil {
		t.Fatal(err)
	}
	runtime := NewRuntime(router, environment, nil)
	request, err := env.NewSetRequest(1, env.SetParams{Name: "TOBY_TEST_SERVICE_ENV", Value: "value"})
	if err != nil {
		t.Fatal(err)
	}
	response, err := runtime.Handle(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	mustOK(t, response)

	request, err = env.NewGetRequest(2)
	if err != nil {
		t.Fatal(err)
	}
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

	request, err = env.NewSetRequest(3, env.SetParams{Name: "TOBY_TEST_SERVICE_ENV", Value: ""})
	if err != nil {
		t.Fatal(err)
	}
	response, err = runtime.Handle(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	mustOK(t, response)
	if got, ok := runtime.getEnvironment("TOBY_TEST_SERVICE_ENV"); ok {
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
