package sandboxmanager

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"petris.dev/toby/internal/control"
)

func TestSandboxManagerHandlesFileCommands(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "context", "file.txt")
	s := NewRuntime(fileRegistry(t))
	request, err := control.NewFileCreateRequest(1, control.FileCreateParams{Path: path, Data: []byte("hello"), Mode: 0o600})
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

	request, err = control.NewFileDeleteRequest(2, control.FileDeleteParams{Path: filepath.Join(dir, "context"), Recursive: true})
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
	s := NewRuntime(fileRegistry(t))
	request, err := control.NewFileCreateRequest(1, control.FileCreateParams{Path: path, Data: []byte("hello")})
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
	request, err = control.NewFileMkdirRequest(2, control.FileMkdirParams{Path: dirPath})
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

func TestCommandEnvironmentListStripsControlEndpoint(t *testing.T) {
	runtime := NewRuntime(nil)
	runtime.setEnvironment(control.EnvControlHost, "127.0.0.1:1234")
	runtime.setEnvironment(control.EnvControlToken, "secret")
	runtime.setEnvironment("KEEP", "value")
	env := runtime.commandEnvironmentList()
	if containsEnv(env, control.EnvControlHost) || containsEnv(env, control.EnvControlToken) {
		t.Fatalf("control environment leaked: %#v", env)
	}
	if !containsEnv(env, "KEEP") {
		t.Fatalf("expected KEEP in env: %#v", env)
	}
}

func TestCommandCredentialRejectsUnresolvedHostSentinels(t *testing.T) {
	if _, err := commandCredential(control.CommandRunParams{UID: control.HostUser}); err == nil {
		t.Fatal("expected unresolved host user to fail")
	}
	if _, err := commandCredential(control.CommandRunParams{GID: control.HostGroup}); err == nil {
		t.Fatal("expected unresolved host group to fail")
	}
}

func TestCommandArgvUsesDefaultShellForForegroundEmptyArgv(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("executable bit test is Unix-specific")
	}
	dir := t.TempDir()
	shell := filepath.Join(dir, "shell")
	if err := os.WriteFile(shell, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	runtime := NewRuntime(nil)
	runtime.setEnvironment("SHELL", shell)
	argv := runtime.commandArgv(control.CommandRunParams{Foreground: true}, runtime.commandEnvironmentSnapshot())
	if len(argv) != 2 || argv[0] != shell || argv[1] != "-i" {
		t.Fatalf("argv = %#v", argv)
	}
}

func TestCommandArgvFallsBackToBinSh(t *testing.T) {
	runtime := NewRuntime(nil)
	runtime.setEnvironment("SHELL", filepath.Join(t.TempDir(), "missing"))
	argv := runtime.commandArgv(control.CommandRunParams{Foreground: true}, runtime.commandEnvironmentSnapshot())
	if len(argv) != 2 || argv[0] != "/bin/sh" || argv[1] != "-i" {
		t.Fatalf("argv = %#v", argv)
	}
}

func TestResolveCommandArgvUsesCommandEnvironmentPath(t *testing.T) {
	dir := t.TempDir()
	command := filepath.Join(dir, "opencode")
	if err := os.WriteFile(command, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	argv, ok := resolveCommandArgv([]string{"opencode", "--version"}, map[string]string{"PATH": dir})
	if !ok {
		t.Fatal("expected command to resolve")
	}
	if len(argv) != 2 || argv[0] != command || argv[1] != "--version" {
		t.Fatalf("argv = %#v", argv)
	}
	if _, ok := resolveCommandArgv([]string{"missing"}, map[string]string{"PATH": dir}); ok {
		t.Fatal("expected missing command not to resolve")
	}
}

func TestRuntimeEnvironmentInitializesFromProcessEnv(t *testing.T) {
	t.Setenv("TOBY_TEST_STARTUP_ENV", "startup")
	runtime := NewRuntime(nil)
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
	runtime := NewRuntime(envRegistry(t))
	request, err := control.NewEnvironmentSetRequest(1, control.EnvironmentSetParams{Name: "TOBY_TEST_SERVICE_ENV", Value: "value"})
	if err != nil {
		t.Fatal(err)
	}
	response, err := runtime.Handle(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	mustOK(t, response)

	request, err = control.NewEnvironmentGetRequest(2)
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
	result, err := control.DecodeEnvironmentGetResult(decoded.Result)
	if err != nil {
		t.Fatal(err)
	}
	if result.Environment["TOBY_TEST_SERVICE_ENV"] != "value" {
		t.Fatalf("environment = %#v", result.Environment)
	}

	request, err = control.NewEnvironmentSetRequest(3, control.EnvironmentSetParams{Name: "TOBY_TEST_SERVICE_ENV", Value: ""})
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

func fileRegistry(t *testing.T) *Registry {
	t.Helper()
	registry, err := NewRegistry(RegistryParams{Services: []Service{FileService{}}})
	if err != nil {
		t.Fatal(err)
	}
	return registry
}

func envRegistry(t *testing.T) *Registry {
	t.Helper()
	registry, err := NewRegistry(RegistryParams{Services: []Service{EnvironmentService{}}})
	if err != nil {
		t.Fatal(err)
	}
	return registry
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

func containsEnv(env []string, name string) bool {
	prefix := name + "="
	for _, item := range env {
		if len(item) >= len(prefix) && item[:len(prefix)] == prefix {
			return true
		}
	}
	return false
}
