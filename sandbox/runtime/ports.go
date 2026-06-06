package runtime

// Parses Docker-style port-publish specs into the ExposedPorts set and
// PortBindings map the container request uses to publish a sandbox port to the
// host. A spec is "[hostIP:][hostPort:]containerPort[/proto]" — the same shape as
// `docker run -p`; the container port (and optional /tcp|/udp protocol) is parsed
// by moby's network.ParsePort, and the optional host IP and port prefix is a
// plain colon-split. Bracketed IPv6 host addresses are not supported.

import (
	"fmt"
	"net/netip"
	"strconv"
	"strings"

	"github.com/moby/moby/api/types/network"
)

// resolvePublishedPorts turns the trimmed `container.ports` / `--publish` specs
// into the exposed-port set and host port-binding map. An empty input resolves to
// nil maps (no ports published).
func resolvePublishedPorts(specs []string) (network.PortSet, network.PortMap, error) {
	if len(specs) == 0 {
		return nil, nil, nil
	}

	exposed := network.PortSet{}
	bindings := network.PortMap{}
	for _, spec := range specs {
		port, binding, err := parsePublishSpec(spec)
		if err != nil {
			return nil, nil, err
		}
		exposed[port] = struct{}{}
		bindings[port] = append(bindings[port], binding)
	}
	return exposed, bindings, nil
}

// parsePublishSpec parses one "[hostIP:][hostPort:]containerPort[/proto]" spec
// into its container Port and the matching host PortBinding. An empty host port
// leaves the daemon to assign one; an empty host IP binds all interfaces.
func parsePublishSpec(spec string) (network.Port, network.PortBinding, error) {
	trimmed := strings.TrimSpace(spec)
	if trimmed == "" {
		return network.Port{}, network.PortBinding{}, fmt.Errorf("invalid published port %q: empty", spec)
	}

	var hostIP, hostPort, containerPort string
	parts := strings.Split(trimmed, ":")
	switch len(parts) {
	case 1:
		containerPort = parts[0]
	case 2:
		hostPort, containerPort = parts[0], parts[1]
	case 3:
		hostIP, hostPort, containerPort = parts[0], parts[1], parts[2]
	default:
		return network.Port{}, network.PortBinding{}, fmt.Errorf("invalid published port %q: too many ':'-separated fields (bracketed IPv6 host addresses are not supported)", spec)
	}

	port, err := network.ParsePort(containerPort)
	if err != nil {
		return network.Port{}, network.PortBinding{}, fmt.Errorf("invalid published port %q: %w", spec, err)
	}

	var binding network.PortBinding
	if hostPort != "" {
		if _, err := strconv.ParseUint(hostPort, 10, 16); err != nil {
			return network.Port{}, network.PortBinding{}, fmt.Errorf("invalid published port %q: invalid host port %q", spec, hostPort)
		}
		binding.HostPort = hostPort
	}
	if hostIP != "" {
		addr, err := netip.ParseAddr(hostIP)
		if err != nil {
			return network.Port{}, network.PortBinding{}, fmt.Errorf("invalid published port %q: invalid host IP %q", spec, hostIP)
		}
		binding.HostIP = addr
	}

	return port, binding, nil
}
