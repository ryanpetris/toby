//go:build linux

package socketrelay

// Verifies host-authority mediation, private endpoint publication, descriptor
// pinning, and synchronous revocation.

import (
	"errors"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"golang.org/x/sys/unix"

	"petris.dev/toby/internal/sandbox/layout"
	"petris.dev/toby/internal/storage/safefs"
)

func TestSetRelaysThroughPrivatePinnedEndpoint(t *testing.T) {
	sourcePath, closeSource := startEchoSocket(t)
	defer closeSource()

	root := openRelayTestRoot(t)
	defer root.Close()
	registry, err := NewRegistry([]Request{{
		HostSocket:    sourcePath,
		SandboxSocket: layout.Runtime + "/docker.sock",
	}})
	if err != nil {
		t.Fatal(err)
	}

	set, err := registry.Start(t.Context(), root, nil)
	if err != nil {
		t.Fatal(err)
	}
	assets := set.RuntimeAssets()
	if len(assets) != 1 ||
		assets[0].Target != layout.Runtime+"/docker.sock" {
		t.Fatalf("runtime assets = %#v", assets)
	}

	info, err := os.Lstat(assets[0].HostPath)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode()&os.ModeSocket == 0 || info.Mode().Perm() != 0o600 {
		t.Fatalf("relay endpoint mode = %v, want socket 0600", info.Mode())
	}
	sources, err := set.Sources()
	if err != nil {
		t.Fatal(err)
	}
	var status unix.Stat_t
	if err := unix.Fstat(
		int(sources[assets[0].Target].Fd()),
		&status,
	); err != nil {
		t.Fatal(err)
	}
	if status.Mode&unix.S_IFMT != unix.S_IFSOCK {
		t.Fatalf("pinned relay source mode = %o", status.Mode)
	}

	client, err := net.DialUnix(
		"unix",
		nil,
		&net.UnixAddr{Name: assets[0].HostPath, Net: "unix"},
	)
	if err != nil {
		t.Fatal(err)
	}
	if err := client.SetDeadline(time.Now().Add(5 * time.Second)); err != nil {
		t.Fatal(err)
	}
	if _, err := client.Write([]byte("host-authority")); err != nil {
		t.Fatal(err)
	}
	response := make([]byte, len("host-authority"))
	if _, err := io.ReadFull(client, response); err != nil {
		t.Fatal(err)
	}
	if string(response) != "host-authority" {
		t.Fatalf("relay response = %q", response)
	}
	if err := client.Close(); err != nil {
		t.Fatal(err)
	}

	if err := set.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(assets[0].HostPath); !errors.Is(
		err,
		os.ErrNotExist,
	) {
		t.Fatalf("relay endpoint remains after Close: %v", err)
	}
}

func TestStartVerifiesHostSocketAccessBeforePublishing(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root bypasses the source socket mode check")
	}

	sourcePath, closeSource := startEchoSocket(t)
	defer closeSource()
	if err := os.Chmod(sourcePath, 0); err != nil {
		t.Fatal(err)
	}

	root := openRelayTestRoot(t)
	defer root.Close()
	registry, err := NewRegistry([]Request{{
		HostSocket:    sourcePath,
		SandboxSocket: layout.Runtime + "/docker.sock",
	}})
	if err != nil {
		t.Fatal(err)
	}

	_, err = registry.Start(t.Context(), root, nil)
	if err == nil || !strings.Contains(err.Error(), "host credentials") {
		t.Fatalf("Start() error = %v, want host credential failure", err)
	}
	entries, readErr := os.ReadDir(root.Path())
	if readErr != nil {
		t.Fatal(readErr)
	}
	if len(entries) != 0 {
		t.Fatalf("failed Start published runtime entries: %#v", entries)
	}
}

func openRelayTestRoot(t *testing.T) *safefs.Directory {
	t.Helper()

	workingDirectory, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	path, err := os.MkdirTemp(
		workingDirectory,
		".toby-socket-relay-test-",
	)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := os.RemoveAll(path); err != nil {
			t.Error(err)
		}
	})
	if err := os.Chmod(path, 0o700); err != nil {
		t.Fatal(err)
	}
	root, err := openRelayDirectoryFile(path)
	if err != nil {
		t.Fatal(err)
	}

	return root
}

func openRelayDirectoryFile(path string) (*safefs.Directory, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()

	return safefs.OpenDirectoryFile(
		file,
		path,
		safefs.DirectoryOptions{
			OwnerUID: os.Geteuid(),
			OwnerGID: os.Getegid(),
		},
	)
}

func startEchoSocket(t *testing.T) (string, func()) {
	t.Helper()

	path := filepath.Join(t.TempDir(), "source.sock")
	listener, err := net.ListenUnix(
		"unix",
		&net.UnixAddr{Name: path, Net: "unix"},
	)
	if err != nil {
		t.Fatal(err)
	}

	var handlers sync.WaitGroup
	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			connection, err := listener.AcceptUnix()
			if err != nil {
				return
			}
			handlers.Add(1)
			go func() {
				defer handlers.Done()
				defer connection.Close()
				_, _ = io.Copy(connection, connection)
			}()
		}
	}()

	return path, func() {
		_ = listener.Close()
		<-done
		handlers.Wait()
	}
}
