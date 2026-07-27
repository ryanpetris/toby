//go:build linux

package sandboxgateway

// Exercises exact socket-generation pinning and launch-side descriptor
// validation for sandbox runtime binding.

import (
	"errors"
	"net"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"

	"petris.dev/toby/internal/sandbox/layout"
)

func TestOpenCapabilityPinsExactSocketGeneration(t *testing.T) {
	path := filepath.Join(t.TempDir(), "gateway.sock")
	listener := listenCapabilitySocket(t, path)

	capability, err := OpenCapability(testDescriptorConfig(t, path))
	if err != nil {
		t.Fatal(err)
	}
	defer capability.Close()

	first, err := capability.File()
	if err != nil {
		t.Fatal(err)
	}
	defer first.Close()
	firstInfo, err := first.Stat()
	if err != nil {
		t.Fatal(err)
	}

	if err := listener.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(path); err != nil &&
		!errors.Is(err, os.ErrNotExist) {
		t.Fatal(err)
	}

	replacement := listenCapabilitySocket(t, path)
	defer replacement.Close()
	second, err := capability.File()
	if err != nil {
		t.Fatal(err)
	}
	defer second.Close()
	secondInfo, err := second.Stat()
	if err != nil {
		t.Fatal(err)
	}
	replacementInfo, err := os.Lstat(path)
	if err != nil {
		t.Fatal(err)
	}

	if !os.SameFile(firstInfo, secondInfo) {
		t.Fatal("capability duplicate changed socket generation")
	}
	if os.SameFile(firstInfo, replacementInfo) {
		t.Fatal("capability followed replacement pathname generation")
	}
}

func TestOpenCapabilityRejectsUnsafeSources(t *testing.T) {
	t.Run("regular file", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "gateway.sock")
		if err := os.WriteFile(path, nil, 0o600); err != nil {
			t.Fatal(err)
		}

		if _, err := OpenCapability(testDescriptorConfig(t, path)); err == nil ||
			!strings.Contains(err.Error(), "not a Unix socket") {
			t.Fatalf("OpenCapability() error = %v", err)
		}
	})

	t.Run("symbolic link", func(t *testing.T) {
		directory := t.TempDir()
		target := filepath.Join(directory, "target.sock")
		listener := listenCapabilitySocket(t, target)
		defer listener.Close()

		link := filepath.Join(directory, "gateway.sock")
		if err := os.Symlink(target, link); err != nil {
			t.Fatal(err)
		}

		if _, err := OpenCapability(testDescriptorConfig(t, link)); err == nil ||
			!strings.Contains(err.Error(), "not a Unix socket") {
			t.Fatalf("OpenCapability() error = %v", err)
		}
	})

	t.Run("non-private mode", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "gateway.sock")
		listener := listenCapabilitySocket(t, path)
		defer listener.Close()
		if err := os.Chmod(path, 0o660); err != nil {
			t.Fatal(err)
		}

		capability, err := OpenCapability(testDescriptorConfig(t, path))
		if err != nil {
			t.Fatal(err)
		}
		if err := capability.Close(); err != nil {
			t.Fatal(err)
		}
	})
}

func TestOpenCapabilityRejectsReplacementBeforePin(t *testing.T) {
	path := filepath.Join(t.TempDir(), "gateway.sock")
	original := listenCapabilitySocket(t, path)
	t.Cleanup(func() {
		if err := original.Close(); err != nil {
			t.Error(err)
		}
	})
	config := testDescriptorConfig(t, path)

	if err := os.Remove(path); err != nil &&
		!errors.Is(err, os.ErrNotExist) {
		t.Fatal(err)
	}
	replacement := listenCapabilitySocket(t, path)
	t.Cleanup(func() {
		if err := replacement.Close(); err != nil {
			t.Error(err)
		}
	})

	if _, err := OpenCapability(config); err == nil ||
		!strings.Contains(err.Error(), "generation changed") {
		t.Fatalf("OpenCapability replacement error = %v", err)
	}
}

func listenCapabilitySocket(t *testing.T, path string) *net.UnixListener {
	t.Helper()

	listener, err := net.ListenUnix(
		"unix",
		&net.UnixAddr{Name: path, Net: "unix"},
	)
	if err != nil {
		t.Fatal(err)
	}
	listener.SetUnlinkOnClose(false)
	if err := os.Chmod(path, 0o600); err != nil {
		listener.Close()
		t.Fatal(err)
	}

	return listener
}

func testDescriptorConfig(t *testing.T, path string) DescriptorConfig {
	t.Helper()

	info, err := os.Lstat(path)
	if err != nil {
		t.Fatal(err)
	}
	status, ok := info.Sys().(*syscall.Stat_t)
	if !ok || status == nil {
		t.Fatal("socket stat identity is unavailable")
	}

	return DescriptorConfig{
		HostSocket:       path,
		HostSocketDevice: uint64(status.Dev),
		HostSocketInode:  status.Ino,
		SandboxSocket:    layout.SandboxSocket(),
	}
}
