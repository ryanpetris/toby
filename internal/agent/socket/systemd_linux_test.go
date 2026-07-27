//go:build linux

package socket

// Exercises systemd activation parsing and inherited listener ownership.

import (
	"errors"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"testing"
)

func TestParseSystemdActivation(t *testing.T) {
	const processID = 4123

	tests := []struct {
		name          string
		listenPID     string
		listenFDs     string
		listenFDNames string
		wantActive    bool
		wantError     bool
	}{
		{
			name:          "named agent listener",
			listenPID:     strconv.Itoa(processID),
			listenFDs:     "1",
			listenFDNames: systemdAgentName,
			wantActive:    true,
		},
		{
			name:       "unnamed listener",
			listenPID:  strconv.Itoa(processID),
			listenFDs:  "1",
			wantActive: true,
		},
		{
			name:       "different process",
			listenPID:  strconv.Itoa(processID + 1),
			listenFDs:  "1",
			wantActive: false,
		},
		{
			name:       "invalid process",
			listenPID:  "not-a-pid",
			listenFDs:  "1",
			wantError:  true,
			wantActive: false,
		},
		{
			name:       "multiple listeners",
			listenPID:  strconv.Itoa(processID),
			listenFDs:  "2",
			wantActive: true,
			wantError:  true,
		},
		{
			name:          "wrong listener name",
			listenPID:     strconv.Itoa(processID),
			listenFDs:     "1",
			listenFDNames: "other",
			wantActive:    true,
			wantError:     true,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			active, err := parseSystemdActivation(
				processID,
				test.listenPID,
				test.listenFDs,
				test.listenFDNames,
			)
			if active != test.wantActive {
				t.Errorf(
					"active = %v, want %v",
					active,
					test.wantActive,
				)
			}
			if (err != nil) != test.wantError {
				t.Errorf(
					"error = %v, want error %v",
					err,
					test.wantError,
				)
			}
		})
	}
}

func TestSystemdListenerIgnoresAnotherProcessAndClearsEnvironment(
	t *testing.T,
) {
	t.Setenv("LISTEN_PID", strconv.Itoa(os.Getpid()+1))
	t.Setenv("LISTEN_FDS", "1")
	t.Setenv("LISTEN_FDNAMES", systemdAgentName)

	listener, active, err := SystemdListener(
		testSocketPath(t),
		Options{},
	)
	if err != nil {
		t.Fatal(err)
	}
	if active || listener != nil {
		t.Fatalf(
			"activation = (%v, %#v), want inactive without listener",
			active,
			listener,
		)
	}
	for _, name := range systemdEnvironmentNames {
		if _, exists := os.LookupEnv(name); exists {
			t.Errorf("%s remains set after activation check", name)
		}
	}
}

func TestAdoptListenerAcceptsPeersAndPreservesSystemdSocket(
	t *testing.T,
) {
	path := testSocketPath(t)
	if err := os.Mkdir(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}

	systemd, err := net.ListenUnix(
		"unix",
		&net.UnixAddr{Name: path, Net: "unix"},
	)
	if err != nil {
		t.Fatal(err)
	}
	systemd.SetUnlinkOnClose(false)
	t.Cleanup(func() {
		if err := systemd.Close(); err != nil &&
			!errors.Is(err, net.ErrClosed) {
			t.Errorf("close systemd listener: %v", err)
		}
		if err := os.Remove(path); err != nil &&
			!errors.Is(err, os.ErrNotExist) {
			t.Errorf("remove systemd socket: %v", err)
		}
	})

	file, err := systemd.File()
	if err != nil {
		t.Fatal(err)
	}
	inherited, err := adoptListener(
		file,
		path,
		Options{},
	)
	closeErr := file.Close()
	if err := errors.Join(err, closeErr); err != nil {
		t.Fatal(err)
	}

	accepted := acceptOne(t, inherited)
	client, err := net.DialUnix(
		"unix",
		nil,
		&net.UnixAddr{Name: path, Net: "unix"},
	)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()

	result := <-accepted
	if result.err != nil {
		t.Fatal(result.err)
	}
	result.conn.Close()

	if err := inherited.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(path); err != nil {
		t.Fatalf("systemd-owned socket was removed: %v", err)
	}
}

func TestAdoptListenerRejectsWrongPath(t *testing.T) {
	path := testSocketPath(t)
	if err := os.Mkdir(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}

	raw, err := net.ListenUnix(
		"unix",
		&net.UnixAddr{Name: path, Net: "unix"},
	)
	if err != nil {
		t.Fatal(err)
	}
	defer raw.Close()

	file, err := raw.File()
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()

	wrongPath := filepath.Join(filepath.Dir(path), "other.sock")
	if _, err := adoptListener(
		file,
		wrongPath,
		Options{},
	); err == nil {
		t.Fatal("adopted listener with the wrong address")
	}
}
