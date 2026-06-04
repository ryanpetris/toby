package docker

import (
	"net"
	"os"
	"strconv"

	"petris.dev/toby/container/manager"
	"petris.dev/toby/internal/control"
	"petris.dev/toby/internal/tools/tool"

	"github.com/testcontainers/testcontainers-go"
)

// controlEnv builds the environment passed to the Setup/Run containers: HOME,
// the control host/token, and TERM. The control host is rewritten for the
// daemon class so the in-sandbox manager can reach the host control server.
func controlEnv(env tool.Environment, class manager.DaemonClass) map[string]string {
	result := map[string]string{}
	if home, ok := env["HOME"]; ok {
		result["HOME"] = home
	}
	if host, ok := env[control.EnvControlHost]; ok {
		result[control.EnvControlHost] = rewriteControlHost(host, class)
	}
	if token, ok := env[control.EnvControlToken]; ok {
		result[control.EnvControlToken] = token
	}
	if term, ok := os.LookupEnv("TERM"); ok && term != "" {
		result["TERM"] = term
	}
	return result
}

// rewriteControlHost swaps 127.0.0.1 for the hostname the container uses to
// reach the host:
//   - local Linux daemon (host networking): unchanged (127.0.0.1).
//   - Docker Desktop: host.docker.internal.
//   - remote/Podman daemon: host.testcontainers.internal (the host-access tunnel).
func rewriteControlHost(value string, class manager.DaemonClass) string {
	switch class {
	case manager.DaemonDesktop:
		return swapHost(value, "host.docker.internal")
	case manager.DaemonRemote:
		return swapHost(value, testcontainers.HostInternal)
	default:
		return value
	}
}

func swapHost(value, host string) string {
	if _, port, err := net.SplitHostPort(value); err == nil {
		return net.JoinHostPort(host, port)
	}
	return value
}

// controlHostPort extracts the host control-server port so it can be exposed to
// the container via the host-access tunnel on remote daemons.
func controlHostPort(env tool.Environment) int {
	host, ok := env[control.EnvControlHost]
	if !ok {
		return 0
	}
	_, port, err := net.SplitHostPort(host)
	if err != nil {
		return 0
	}
	value, err := strconv.Atoi(port)
	if err != nil {
		return 0
	}
	return value
}

func networkLabel(class manager.DaemonClass, phase phaseKind) string {
	if phase == phasePrime {
		return ""
	}
	if class == manager.DaemonLocalUnix {
		return "host"
	}
	return "bridge"
}
