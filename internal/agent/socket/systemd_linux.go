//go:build linux

package socket

// Adopts the single agent listener supplied by systemd socket activation.

import (
	"errors"
	"fmt"
	"net"
	"os"
	"strconv"
)

const (
	systemdFirstFileDescriptor = 3
	systemdAgentName           = "agent"
)

var systemdEnvironmentNames = [...]string{
	"LISTEN_PID",
	"LISTEN_FDS",
	"LISTEN_FDNAMES",
}

// SystemdListener consumes and adopts a systemd-activated agent listener.
// The boolean result is false when the process was not started by systemd.
func SystemdListener(
	path string,
	options Options,
) (*Listener, bool, error) {
	listenPID, listenPIDSet := os.LookupEnv(systemdEnvironmentNames[0])
	listenFDs, listenFDsSet := os.LookupEnv(systemdEnvironmentNames[1])
	listenFDNames, listenFDNamesSet := os.LookupEnv(
		systemdEnvironmentNames[2],
	)
	if !listenPIDSet && !listenFDsSet && !listenFDNamesSet {
		return nil, false, nil
	}

	var unsetErr error
	for _, name := range systemdEnvironmentNames {
		unsetErr = errors.Join(unsetErr, os.Unsetenv(name))
	}
	options.Logger.DebugError(
		"clear systemd socket activation environment",
		unsetErr,
	)

	activated, err := parseSystemdActivation(
		os.Getpid(),
		listenPID,
		listenFDs,
		listenFDNames,
	)
	if err != nil || !activated {
		return nil, activated, err
	}

	file := os.NewFile(
		uintptr(systemdFirstFileDescriptor),
		"systemd agent listener",
	)
	if file == nil {
		return nil, true, fmt.Errorf(
			"adopt systemd agent listener: descriptor %d is invalid",
			systemdFirstFileDescriptor,
		)
	}

	listener, adoptErr := adoptListener(
		file,
		path,
		options,
	)
	options.Logger.DebugError(
		"close inherited systemd listener descriptor",
		file.Close(),
	)
	if adoptErr != nil {
		if listener != nil {
			options.Logger.DebugError(
				"close rejected systemd listener",
				listener.Close(),
			)
		}
		return nil, true, adoptErr
	}

	return listener, true, nil
}

func parseSystemdActivation(
	processID int,
	listenPID string,
	listenFDs string,
	listenFDNames string,
) (bool, error) {
	activationPID, err := strconv.Atoi(listenPID)
	if err != nil || activationPID <= 0 {
		return false, fmt.Errorf(
			"parse systemd LISTEN_PID %q: expected a positive process ID",
			listenPID,
		)
	}
	if activationPID != processID {
		return false, nil
	}

	descriptorCount, err := strconv.Atoi(listenFDs)
	if err != nil || descriptorCount != 1 {
		return true, fmt.Errorf(
			"parse systemd LISTEN_FDS %q: Toby requires exactly one listener",
			listenFDs,
		)
	}
	if listenFDNames != "" && listenFDNames != systemdAgentName {
		return true, fmt.Errorf(
			"parse systemd LISTEN_FDNAMES %q: expected %q",
			listenFDNames,
			systemdAgentName,
		)
	}

	return true, nil
}

func adoptListener(
	file *os.File,
	path string,
	options Options,
) (*Listener, error) {
	if file == nil {
		return nil, fmt.Errorf("systemd agent listener file is nil")
	}
	if _, _, err := validateSocketPath(path); err != nil {
		return nil, err
	}

	generic, err := net.FileListener(file)
	if err != nil {
		return nil, fmt.Errorf("adopt systemd agent listener: %w", err)
	}
	raw, ok := generic.(*net.UnixListener)
	if !ok {
		options.Logger.DebugError(
			"close rejected systemd listener",
			generic.Close(),
		)
		return nil, fmt.Errorf(
			"adopt systemd agent listener: descriptor is %T, want Unix listener",
			generic,
		)
	}
	raw.SetUnlinkOnClose(false)

	address, ok := raw.Addr().(*net.UnixAddr)
	if !ok || address.Name != path {
		options.Logger.DebugError(
			"close mismatched systemd listener",
			raw.Close(),
		)
		return nil, fmt.Errorf(
			"adopt systemd agent listener: address is %v, want %q",
			raw.Addr(),
			path,
		)
	}

	return &Listener{
		raw:     raw,
		address: &net.UnixAddr{Name: path, Net: "unix"},
	}, nil
}
