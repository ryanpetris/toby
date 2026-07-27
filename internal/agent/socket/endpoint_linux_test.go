//go:build linux

package socket

// Verifies private agent endpoint creation, connection, and stale recovery.

import (
	"context"
	"errors"
	"net"
	"os"
	"path/filepath"
	"sync"
	"syscall"
	"testing"
	"time"
)

func TestElectCreatesOwnerOnlyEndpoint(t *testing.T) {
	path := testSocketPath(t)
	listener := mustElectListener(t, path)

	parentInfo, err := os.Stat(filepath.Dir(path))
	if err != nil {
		t.Fatalf("stat endpoint directory: %v", err)
	}
	if got := parentInfo.Mode().Perm(); got != 0o700 {
		t.Errorf("endpoint directory mode = %04o, want 0700", got)
	}
	parentStat, ok := parentInfo.Sys().(*syscall.Stat_t)
	if !ok {
		t.Fatalf("endpoint directory stat type = %T, want *syscall.Stat_t", parentInfo.Sys())
	}
	if got, want := parentStat.Uid, uint32(os.Geteuid()); got != want {
		t.Errorf("endpoint directory UID = %d, want %d", got, want)
	}

	socketInfo, err := os.Lstat(path)
	if err != nil {
		t.Fatalf("stat endpoint socket: %v", err)
	}
	if socketInfo.Mode()&os.ModeSocket == 0 {
		t.Errorf("endpoint mode = %v, want Unix socket", socketInfo.Mode())
	}
	if got := socketInfo.Mode().Perm(); got != 0o600 {
		t.Errorf("endpoint socket mode = %04o, want 0600", got)
	}
	socketStat, ok := socketInfo.Sys().(*syscall.Stat_t)
	if !ok {
		t.Fatalf("endpoint socket stat type = %T, want *syscall.Stat_t", socketInfo.Sys())
	}
	if got, want := socketStat.Uid, uint32(os.Geteuid()); got != want {
		t.Errorf("endpoint socket UID = %d, want %d", got, want)
	}
	if got := listener.Addr().String(); got != path {
		t.Errorf("listener address = %q, want %q", got, path)
	}

	client, server := dialAndAccept(t, path, listener)
	client.Close()
	server.Close()
}

func TestElectConnectsToActiveListenerWithoutReplacingIt(t *testing.T) {
	path := testSocketPath(t)
	listener := mustElectListener(t, path)

	before, err := os.Lstat(path)
	if err != nil {
		t.Fatalf("stat socket before second election: %v", err)
	}

	election, err := Elect(t.Context(), path, Options{})
	if err != nil {
		t.Fatalf("run second election: %v", err)
	}
	if election.Listener != nil || election.Conn == nil {
		t.Fatalf("second election = %#v, want connection only", election)
	}
	defer election.Conn.Close()

	after, err := os.Lstat(path)
	if err != nil {
		t.Fatalf("stat socket after second election: %v", err)
	}
	beforeStat := before.Sys().(*syscall.Stat_t)
	afterStat := after.Sys().(*syscall.Stat_t)
	if beforeStat.Dev != afterStat.Dev || beforeStat.Ino != afterStat.Ino {
		t.Fatalf(
			"active socket changed from %d:%d to %d:%d",
			beforeStat.Dev,
			beforeStat.Ino,
			afterStat.Dev,
			afterStat.Ino,
		)
	}

	accepted, err := listener.Accept()
	if err != nil {
		t.Fatalf("accept election connection: %v", err)
	}
	accepted.Close()
}

func TestElectReplacesOnlyStaleSocket(t *testing.T) {
	path := testSocketPath(t)
	parent := filepath.Dir(path)
	if err := os.Mkdir(parent, 0o700); err != nil {
		t.Fatalf("create endpoint parent: %v", err)
	}

	stale, err := net.ListenUnix("unix", &net.UnixAddr{Name: path, Net: "unix"})
	if err != nil {
		t.Fatalf("create stale listener: %v", err)
	}
	stale.SetUnlinkOnClose(false)
	if err := os.Chmod(path, 0o600); err != nil {
		stale.Close()
		t.Fatalf("secure stale socket: %v", err)
	}
	if err := stale.Close(); err != nil {
		t.Fatalf("close stale listener: %v", err)
	}

	listener := mustElectListener(t, path)
	client, server := dialAndAccept(t, path, listener)
	client.Close()
	server.Close()
}

func TestListenerClosePreservesReplacementGeneration(t *testing.T) {
	path := testSocketPath(t)
	election, err := Elect(t.Context(), path, Options{})
	if err != nil {
		t.Fatalf("elect original listener: %v", err)
	}
	original := election.Listener

	if err := os.Remove(path); err != nil {
		t.Fatalf("unlink original socket pathname: %v", err)
	}

	replacement, err := net.ListenUnix("unix", &net.UnixAddr{Name: path, Net: "unix"})
	if err != nil {
		t.Fatalf("listen on replacement socket: %v", err)
	}
	replacement.SetUnlinkOnClose(false)
	defer func() {
		replacement.Close()
		os.Remove(path)
	}()
	if err := os.Chmod(path, 0o600); err != nil {
		t.Fatalf("secure replacement socket: %v", err)
	}

	if err := original.Close(); err != nil {
		t.Fatalf("close original listener: %v", err)
	}
	if _, err := os.Lstat(path); err != nil {
		t.Fatalf("replacement socket was removed: %v", err)
	}

	accepted := make(chan error, 1)
	go func() {
		conn, err := replacement.AcceptUnix()
		if err == nil {
			conn.Close()
		}
		accepted <- err
	}()

	conn, err := Dial(t.Context(), path, Options{})
	if err != nil {
		t.Fatalf("dial replacement socket: %v", err)
	}
	conn.Close()
	if err := <-accepted; err != nil {
		t.Fatalf("accept replacement connection: %v", err)
	}
}

func TestListenerCloseRemovesItsSocketGeneration(t *testing.T) {
	path := testSocketPath(t)
	election, err := Elect(t.Context(), path, Options{})
	if err != nil {
		t.Fatalf("elect listener: %v", err)
	}

	if err := election.Listener.Close(); err != nil {
		t.Fatalf("close listener: %v", err)
	}
	if _, err := os.Lstat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("socket remains after close: %v", err)
	}
	if err := election.Listener.Close(); err != nil {
		t.Fatalf("second close: %v", err)
	}
}

func TestListenerFileRetainsExactSocketGeneration(t *testing.T) {
	path := testSocketPath(t)
	listener := mustElectListener(t, path)

	device, inode := listener.Generation()
	file, err := listener.File()
	if err != nil {
		t.Fatalf("retain listener socket: %v", err)
	}
	defer file.Close()

	info, err := file.Stat()
	if err != nil {
		t.Fatalf("inspect retained listener socket: %v", err)
	}
	status, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		t.Fatalf(
			"retained socket stat type = %T, want *syscall.Stat_t",
			info.Sys(),
		)
	}
	if info.Mode()&os.ModeSocket == 0 ||
		uint64(status.Dev) != device ||
		status.Ino != inode {
		t.Fatalf(
			"retained socket identity = %d:%d mode %v, want %d:%d socket",
			status.Dev,
			status.Ino,
			info.Mode(),
			device,
			inode,
		)
	}

	if err := listener.Close(); err != nil {
		t.Fatalf("close listener: %v", err)
	}
	if _, err := listener.File(); !errors.Is(err, net.ErrClosed) {
		t.Fatalf("File after close = %v, want net.ErrClosed", err)
	}
	if _, err := file.Stat(); err != nil {
		t.Fatalf(
			"retained socket descriptor did not survive unlink: %v",
			err,
		)
	}
}

func TestConcurrentElectionProducesOneListener(t *testing.T) {
	const contenders = 16

	path := testSocketPath(t)
	start := make(chan struct{})
	results := make(chan electionResult, contenders)

	var group sync.WaitGroup
	for range contenders {
		group.Add(1)
		go func() {
			defer group.Done()
			<-start

			election, err := Elect(t.Context(), path, Options{})
			results <- electionResult{election: election, err: err}
		}()
	}

	close(start)
	group.Wait()
	close(results)

	var listener *Listener
	var clients []*net.UnixConn
	for result := range results {
		if result.err != nil {
			t.Fatalf("elect agent endpoint: %v", result.err)
		}
		switch {
		case result.election.Listener != nil && result.election.Conn == nil:
			if listener != nil {
				t.Fatal("more than one contender won listener election")
			}
			listener = result.election.Listener
		case result.election.Listener == nil && result.election.Conn != nil:
			clients = append(clients, result.election.Conn)
		default:
			t.Fatalf("invalid election result: %#v", result.election)
		}
	}
	if listener == nil {
		t.Fatal("no contender won listener election")
	}
	defer listener.Close()
	if got := len(clients); got != contenders-1 {
		t.Fatalf("connected contenders = %d, want %d", got, contenders-1)
	}

	for _, client := range clients {
		server, err := listener.Accept()
		if err != nil {
			t.Fatalf("accept contender: %v", err)
		}
		server.Close()
		client.Close()
	}
}

func TestSocketOperationsHonorCancellation(t *testing.T) {
	path := testSocketPath(t)
	mustElectListener(t, path)

	canceled, cancel := context.WithCancel(t.Context())
	cancel()

	if _, err := Dial(canceled, path, Options{}); !errors.Is(err, context.Canceled) {
		t.Errorf("canceled Dial error = %v, want context.Canceled", err)
	}
}

func TestElectionLockWaitHonorsDeadline(t *testing.T) {
	path := testSocketPath(t)
	parent := filepath.Dir(path)
	if err := os.Mkdir(parent, 0o700); err != nil {
		t.Fatalf("create endpoint parent: %v", err)
	}

	directory, err := openEndpointDirectory(
		path,
		false,
		uint32(os.Geteuid()),
		uint32(os.Getegid()),
		Options{},
	)
	if err != nil {
		t.Fatalf("open endpoint directory: %v", err)
	}
	defer directory.close()

	guard, err := directory.lock(t.Context())
	if err != nil {
		t.Fatalf("hold election lock: %v", err)
	}
	defer guard.close()

	ctx, cancel := context.WithTimeout(t.Context(), 25*time.Millisecond)
	defer cancel()

	if _, err := Elect(ctx, path, Options{}); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("blocked election error = %v, want context deadline", err)
	}
}

type electionResult struct {
	election *Election
	err      error
}

func TestElectReplacesPermissiveStaleSocket(t *testing.T) {
	path := testSocketPath(t)
	parent := filepath.Dir(path)
	if err := os.Mkdir(parent, 0o700); err != nil {
		t.Fatalf("create endpoint parent: %v", err)
	}

	stale, err := net.ListenUnix("unix", &net.UnixAddr{Name: path, Net: "unix"})
	if err != nil {
		t.Fatalf("create stale listener: %v", err)
	}
	stale.SetUnlinkOnClose(false)
	if err := os.Chmod(path, 0o660); err != nil {
		stale.Close()
		t.Fatalf("make stale socket unsafe: %v", err)
	}
	if err := stale.Close(); err != nil {
		t.Fatalf("close stale listener: %v", err)
	}

	election, err := Elect(t.Context(), path, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if election.Listener == nil {
		t.Fatal("election did not replace the stale socket")
	}
	if err := election.Listener.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestDialConnectsThroughNonWritableSystemdDirectory(t *testing.T) {
	directory := filepath.Join(t.TempDir(), "systemd")
	if err := os.Mkdir(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(directory, "agent.sock")

	listener, err := net.ListenUnix(
		"unix",
		&net.UnixAddr{Name: path, Net: "unix"},
	)
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()

	if err := os.Chmod(directory, 0o500); err != nil {
		t.Fatal(err)
	}
	defer os.Chmod(directory, 0o700)

	accepted := make(chan error, 1)
	go func() {
		connection, err := listener.AcceptUnix()
		if err == nil {
			err = connection.Close()
		}
		accepted <- err
	}()

	connection, err := Dial(t.Context(), path, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if err := connection.Close(); err != nil {
		t.Fatal(err)
	}
	if err := <-accepted; err != nil {
		t.Fatal(err)
	}
}
