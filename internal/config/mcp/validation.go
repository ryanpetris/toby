package mcpconfig

// Enforces transport-specific fields, resource-sharing policy, and safe host
// boundary values for native MCP definitions.

import (
	"fmt"
	"net/http"
	"net/url"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"unicode/utf8"

	"petris.dev/toby/internal/agent/resource"
	"petris.dev/toby/internal/mcpgateway/httpbridge"
	"petris.dev/toby/internal/sandbox/layout"
	"petris.dev/toby/internal/sandbox/mount"
)

const builtinServerName = "toby"

var environmentNamePattern = regexp.MustCompile(
	`^[A-Za-z_][A-Za-z0-9_]*$`,
)

// Validate rejects ambiguous definitions and policies that could share a
// backend more broadly than its mounted data permits.
func (c Config) Validate() error {
	if len(c.Servers) > maxServers {
		return fmt.Errorf(
			"resources.mcps count exceeds %d",
			maxServers,
		)
	}
	if _, exists := c.Servers[builtinServerName]; exists {
		return fmt.Errorf(
			"resources.mcps.%s is reserved for Toby",
			builtinServerName,
		)
	}

	for _, name := range sortedNames(c.Servers) {
		if err := validateServerName(name); err != nil {
			return err
		}
		if err := c.Servers[name].validate(name); err != nil {
			return err
		}
	}

	return nil
}

func (s Server) validate(name string) error {
	switch s.Type {
	case ServerRemote:
		return s.validateRemote(name)
	case ServerLocal:
		return s.validateLocal(name)
	default:
		return fmt.Errorf(
			"resources.mcps.%s has unsupported type %q",
			name,
			s.Type,
		)
	}
}

func (s Server) validateRemote(name string) error {
	if s.Transport != TransportHTTP {
		return fmt.Errorf(
			"resources.mcps.%s: remote servers must use http transport",
			name,
		)
	}
	if err := validateRemoteURL(s.URL); err != nil {
		return fmt.Errorf("resources.mcps.%s.url: %w", name, err)
	}
	if err := validateHeaders(s.Headers); err != nil {
		return fmt.Errorf("resources.mcps.%s.headers: %w", name, err)
	}
	if s.Image != "" ||
		len(s.Command) != 0 ||
		len(s.Environment) != 0 ||
		s.Endpoint != nil ||
		len(s.Mounts) != 0 ||
		s.Scope != "" ||
		s.Network != "" ||
		s.IdleTimeout != 0 {
		return fmt.Errorf(
			"resources.mcps.%s: remote server contains local-only fields",
			name,
		)
	}

	return nil
}

func (s Server) validateLocal(name string) error {
	if s.URL != "" || len(s.Headers) != 0 {
		return fmt.Errorf(
			"resources.mcps.%s: local server contains remote-only fields",
			name,
		)
	}
	if strings.TrimSpace(s.Image) == "" ||
		!utf8.ValidString(s.Image) ||
		strings.TrimSpace(s.Image) != s.Image ||
		strings.ContainsRune(s.Image, 0) {
		return fmt.Errorf(
			"resources.mcps.%s.image must be a non-empty OCI reference without surrounding whitespace",
			name,
		)
	}
	if len(s.Command) == 0 || strings.TrimSpace(s.Command[0]) == "" {
		return fmt.Errorf(
			"resources.mcps.%s.command must contain an executable",
			name,
		)
	}
	for index, argument := range s.Command {
		if !utf8.ValidString(argument) ||
			strings.ContainsRune(argument, 0) {
			return fmt.Errorf(
				"resources.mcps.%s.command[%d] is invalid",
				name,
				index,
			)
		}
	}
	if err := validateEnvironment(s.Environment); err != nil {
		return fmt.Errorf(
			"resources.mcps.%s.environment: %w",
			name,
			err,
		)
	}
	if err := validateMounts(s.Mounts); err != nil {
		return fmt.Errorf("resources.mcps.%s.mounts: %w", name, err)
	}
	if s.Scope == "" {
		return fmt.Errorf(
			"resources.mcps.%s.scope is required",
			name,
		)
	}
	if _, err := s.EffectiveScope(); err != nil {
		return fmt.Errorf("resources.mcps.%s.scope: %w", name, err)
	}
	switch s.Network {
	case resource.NetworkHost, resource.NetworkPrivate:
	default:
		return fmt.Errorf(
			"resources.mcps.%s.network has unsupported value %q",
			name,
			s.Network,
		)
	}

	switch s.Transport {
	case TransportStdio:
		if s.Endpoint != nil {
			return fmt.Errorf(
				"resources.mcps.%s: stdio server must not define an endpoint",
				name,
			)
		}
		if s.IdleTimeout != 0 {
			return fmt.Errorf(
				"resources.mcps.%s: stdio server must not define idleTimeout",
				name,
			)
		}
	case TransportHTTP:
		if s.Endpoint == nil {
			return fmt.Errorf(
				"resources.mcps.%s: http server requires an endpoint",
				name,
			)
		}
		if err := s.Endpoint.validate(); err != nil {
			return fmt.Errorf(
				"resources.mcps.%s.endpoint: %w",
				name,
				err,
			)
		}
		if s.IdleTimeout < 0 {
			return fmt.Errorf(
				"resources.mcps.%s.idleTimeout must not be negative",
				name,
			)
		}
	default:
		return fmt.Errorf(
			"resources.mcps.%s has unsupported transport %q",
			name,
			s.Transport,
		)
	}

	return nil
}

func (e Endpoint) validate() error {
	if err := httpbridge.ValidateRequestPath(e.Path); err != nil {
		return err
	}

	switch e.Kind {
	case EndpointUnix:
		if e.Socket == "" ||
			!utf8.ValidString(e.Socket) ||
			strings.ContainsRune(e.Socket, 0) ||
			!path.IsAbs(e.Socket) ||
			path.Clean(e.Socket) != e.Socket {
			return fmt.Errorf(
				"unix endpoint socket must be a clean absolute path",
			)
		}
		if path.Dir(e.Socket) != layout.Runtime ||
			path.Base(e.Socket) == "." {
			return fmt.Errorf(
				"unix endpoint socket must be directly beneath %s",
				layout.Runtime,
			)
		}
	default:
		return fmt.Errorf("unsupported kind %q", e.Kind)
	}

	return nil
}

func validateRemoteURL(value string) error {
	if value == "" ||
		!utf8.ValidString(value) ||
		strings.TrimSpace(value) != value {
		return fmt.Errorf(
			"must be a non-empty URL without surrounding whitespace",
		)
	}

	parsed, err := url.Parse(value)
	if err != nil {
		return fmt.Errorf("invalid URL: %w", err)
	}
	if parsed.Scheme != "https" && parsed.Scheme != "http" {
		return fmt.Errorf("scheme must be http or https")
	}
	if parsed.Host == "" ||
		parsed.User != nil ||
		parsed.Fragment != "" {
		return fmt.Errorf(
			"must contain a host and no user information or fragment",
		)
	}

	return nil
}

func validateHeaders(headers map[string]string) error {
	values := make(http.Header, len(headers))
	for _, name := range sortedNames(headers) {
		value := headers[name]
		if !utf8.ValidString(name) || !utf8.ValidString(value) {
			return fmt.Errorf("header %q contains invalid UTF-8", name)
		}

		values[name] = []string{value}
	}

	return httpbridge.ValidateConfiguredHeaders(values)
}

func validateEnvironment(environment map[string]string) error {
	for _, name := range sortedNames(environment) {
		value := environment[name]
		if !environmentNamePattern.MatchString(name) {
			return fmt.Errorf(
				"invalid environment variable name %q",
				name,
			)
		}
		if !utf8.ValidString(value) {
			return fmt.Errorf(
				"environment variable %q contains invalid UTF-8",
				name,
			)
		}
		if strings.ContainsRune(value, 0) {
			return fmt.Errorf(
				"environment variable %q contains NUL",
				name,
			)
		}
		if name == "HOME" || name == "TOBY_SANDBOX" {
			return fmt.Errorf(
				"environment variable %q is runtime-controlled",
				name,
			)
		}
	}

	return nil
}

func validateMounts(mounts []Mount) error {
	for index, item := range mounts {
		if item.Source == "" ||
			!utf8.ValidString(item.Source) ||
			!filepath.IsAbs(item.Source) ||
			filepath.Clean(item.Source) != item.Source ||
			strings.ContainsRune(item.Source, 0) {
			return fmt.Errorf(
				"mount %d source must be a clean absolute host path",
				index,
			)
		}
		if !utf8.ValidString(item.Target) {
			return fmt.Errorf(
				"mount %d target contains invalid UTF-8",
				index,
			)
		}
		if err := mount.ValidateTarget(item.Target); err != nil {
			return fmt.Errorf("mount %d target: %w", index, err)
		}
		for _, reserved := range []string{
			"/proc",
			"/dev",
			"/tmp",
			"/run",
		} {
			if mount.TargetsOverlap(item.Target, reserved) {
				return fmt.Errorf(
					"mount %d target overlaps reserved path %q",
					index,
					reserved,
				)
			}
		}
		switch item.Access {
		case mount.AccessRegular, mount.AccessReadOnly:
		default:
			return fmt.Errorf(
				"mount %d has unsupported access %q",
				index,
				item.Access,
			)
		}
		switch item.Scope {
		case resource.ScopeHome, resource.ScopeProject:
		default:
			return fmt.Errorf(
				"mount %d has unsupported scope %q",
				index,
				item.Scope,
			)
		}

		for prior := 0; prior < index; prior++ {
			if mount.TargetsOverlap(
				mounts[prior].Target,
				item.Target,
			) {
				return fmt.Errorf(
					"mount %d target %q overlaps mount %d target %q",
					index,
					item.Target,
					prior,
					mounts[prior].Target,
				)
			}
		}
	}

	return nil
}

func validateServerName(name string) error {
	if name == "" ||
		len(name) > 256 ||
		!utf8.ValidString(name) {
		return fmt.Errorf("invalid MCP server name %q", name)
	}
	for _, character := range name {
		if (character >= 'a' && character <= 'z') ||
			(character >= 'A' && character <= 'Z') ||
			(character >= '0' && character <= '9') ||
			character == '.' ||
			character == '_' ||
			character == '-' ||
			character == ':' {
			continue
		}

		return fmt.Errorf("invalid MCP server name %q", name)
	}

	return nil
}

func sortedNames[T any](values map[string]T) []string {
	names := make([]string, 0, len(values))
	for name := range values {
		names = append(names, name)
	}
	sort.Strings(names)

	return names
}
