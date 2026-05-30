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

func TestCommandArgvUsesDefaultShellForForegroundEmptyArgv(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("executable bit test is Unix-specific")
	}
	dir := t.TempDir()
	shell := filepath.Join(dir, "shell")
	if err := os.WriteFile(shell, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("SHELL", shell)
	argv := commandArgv(control.CommandRunParams{Foreground: true})
	if len(argv) != 2 || argv[0] != shell || argv[1] != "-i" {
		t.Fatalf("argv = %#v", argv)
	}
}

func TestCommandArgvFallsBackToBinSh(t *testing.T) {
	t.Setenv("SHELL", filepath.Join(t.TempDir(), "missing"))
	argv := commandArgv(control.CommandRunParams{Foreground: true})
	if len(argv) != 2 || argv[0] != "/bin/sh" || argv[1] != "-i" {
		t.Fatalf("argv = %#v", argv)
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
