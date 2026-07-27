//go:build linux

package run

// Verifies that NativeRun consumes and revokes run-scoped socket relay
// authority on construction failure and normal teardown.

import (
	"errors"
	"net"
	"os"
	"path/filepath"
	"testing"

	"petris.dev/toby/internal/sandbox/layout"
	"petris.dev/toby/internal/socketrelay"
)

func TestNativeRunOwnsSocketRelayLifetime(t *testing.T) {
	tests := []struct {
		name               string
		failConstruction   bool
		wantConstructionOK bool
	}{
		{name: "construction failure", failConstruction: true},
		{name: "normal teardown", wantConstructionOK: true},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			input, _, _ := nativeOwnershipInput(t)
			runtimeRoot, err := openNativeRuntimeRoot(
				input.Directories,
				os.Geteuid(),
				os.Getegid(),
				nil,
			)
			if err != nil {
				t.Fatal(err)
			}
			defer runtimeRoot.Close()

			sourcePath := filepath.Join(t.TempDir(), "source.sock")
			source, err := net.ListenUnix(
				"unix",
				&net.UnixAddr{Name: sourcePath, Net: "unix"},
			)
			if err != nil {
				t.Fatal(err)
			}
			defer source.Close()

			registry, err := socketrelay.NewRegistry(
				[]socketrelay.Request{{
					HostSocket:    sourcePath,
					SandboxSocket: layout.Runtime + "/docker.sock",
				}},
			)
			if err != nil {
				t.Fatal(err)
			}
			relays, err := registry.Start(
				t.Context(),
				runtimeRoot,
				nil,
			)
			if err != nil {
				t.Fatal(err)
			}
			endpoint := relays.RuntimeAssets()[0].HostPath
			input.SocketRelays = relays

			if test.failConstruction {
				if err := input.SandboxBinary.Close(); err != nil {
					t.Fatal(err)
				}
			}

			native, err := NewNativeRun(t.Context(), input)
			if test.wantConstructionOK {
				if err != nil {
					t.Fatal(err)
				}
				if err := native.Close(); err != nil {
					t.Fatal(err)
				}
			} else if err == nil {
				t.Fatal("NewNativeRun unexpectedly succeeded")
			}

			if _, err := relays.Sources(); !errors.Is(
				err,
				os.ErrClosed,
			) {
				t.Fatalf("relay sources remain authoritative: %v", err)
			}
			if _, err := os.Lstat(endpoint); !errors.Is(
				err,
				os.ErrNotExist,
			) {
				t.Fatalf("relay endpoint remains after teardown: %v", err)
			}
		})
	}
}

func TestNativeRunConstructionFailureRevokesAuthorityAndRunStorage(
	t *testing.T,
) {
	tests := []struct {
		name       string
		failAttach bool
	}{
		{name: "source assembly failure"},
		{name: "tool sandbox attachment failure", failAttach: true},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			input, _, runRoot := nativeOwnershipInput(t)
			if test.failAttach {
				attachedInput, _, _ := nativeOwnershipInput(t)
				attached, err := NewNativeRun(t.Context(), attachedInput)
				if err != nil {
					t.Fatal(err)
				}
				if err := attached.Close(); err != nil {
					t.Fatal(err)
				}
				input.ToolSandbox = attachedInput.ToolSandbox
			} else if err := input.SandboxBinary.Close(); err != nil {
				t.Fatal(err)
			}

			runtimeRoot, err := openNativeRuntimeRoot(
				input.Directories,
				os.Geteuid(),
				os.Getegid(),
				nil,
			)
			if err != nil {
				t.Fatal(err)
			}
			defer runtimeRoot.Close()

			sourcePath := filepath.Join(t.TempDir(), "source.sock")
			source, err := net.ListenUnix(
				"unix",
				&net.UnixAddr{Name: sourcePath, Net: "unix"},
			)
			if err != nil {
				t.Fatal(err)
			}
			defer source.Close()

			registry, err := socketrelay.NewRegistry(
				[]socketrelay.Request{{
					HostSocket:    sourcePath,
					SandboxSocket: layout.Runtime + "/docker.sock",
				}},
			)
			if err != nil {
				t.Fatal(err)
			}
			relays, err := registry.Start(
				t.Context(),
				runtimeRoot,
				nil,
			)
			if err != nil {
				t.Fatal(err)
			}
			endpoint := relays.RuntimeAssets()[0].HostPath
			input.SocketRelays = relays

			if _, err := NewNativeRun(t.Context(), input); err == nil {
				t.Fatal("NewNativeRun unexpectedly succeeded")
			}
			if _, err := relays.Sources(); !errors.Is(
				err,
				os.ErrClosed,
			) {
				t.Fatalf(
					"socket relays remain authoritative: %v",
					err,
				)
			}
			if _, err := os.Lstat(endpoint); !errors.Is(
				err,
				os.ErrNotExist,
			) {
				t.Fatalf("relay endpoint remains after failure: %v", err)
			}
			if _, err := os.Stat(runRoot); !errors.Is(
				err,
				os.ErrNotExist,
			) {
				t.Fatalf("run directories remain after failure: %v", err)
			}
		})
	}
}
