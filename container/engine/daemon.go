package engine

// Daemon classification: how the Docker daemon is reached, which drives the
// sandbox networking policy (host networking vs. host-access tunnel) and the
// control-host rewrite. classifyDaemon derives the class from the daemon host.

import (
	"net/url"
	"runtime"
)

// DaemonClass describes how the Docker daemon is reached.
type DaemonClass int

const (
	// DaemonLocalUnix is a daemon reached over a local unix socket on Linux:
	// containers can share the host network namespace.
	DaemonLocalUnix DaemonClass = iota
	// DaemonDesktop is Docker Desktop (macOS/Windows): bridge networking with a
	// magic host.docker.internal hostname.
	DaemonDesktop
	// DaemonRemote is any remote daemon (tcp/ssh) or Podman over the network:
	// the host is only reachable via testcontainers' host-access tunnel.
	DaemonRemote
)

func classifyDaemon(host string) DaemonClass {
	if runtime.GOOS == "darwin" || runtime.GOOS == "windows" {
		return DaemonDesktop
	}

	scheme := ""
	if u, err := url.Parse(host); err == nil {
		scheme = u.Scheme
	}
	switch scheme {
	case "unix", "npipe", "":
		return DaemonLocalUnix
	default:
		return DaemonRemote
	}
}
