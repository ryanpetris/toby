package command

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"petris.dev/toby/control"
)

func TestCommandCredentialRejectsUnresolvedHostSentinels(t *testing.T) {
	if _, err := commandCredential(RunParams{UID: control.HostUser}); err == nil {
		t.Fatal("expected unresolved host user to fail")
	}
	if _, err := commandCredential(RunParams{GID: control.HostGroup}); err == nil {
		t.Fatal("expected unresolved host group to fail")
	}
}

func TestCommandCredentialIgnoresCredentialChangesAsNonRoot(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("non-root credential behavior")
	}
	credential, err := commandCredential(RunParams{UID: 0, GID: 0, Groups: []int{0}})
	if err != nil {
		t.Fatal(err)
	}
	if credential != nil {
		t.Fatalf("credential = %#v, want nil", credential)
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
	s := New(nil)
	argv := s.commandArgv(RunParams{Foreground: true}, map[string]string{"SHELL": shell})
	if len(argv) != 2 || argv[0] != shell || argv[1] != "-i" {
		t.Fatalf("argv = %#v", argv)
	}
}

func TestCommandArgvFallsBackToBinSh(t *testing.T) {
	s := New(nil)
	argv := s.commandArgv(RunParams{Foreground: true}, map[string]string{"SHELL": filepath.Join(t.TempDir(), "missing")})
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
