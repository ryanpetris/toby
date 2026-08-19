package mcpconfig

// Defines the strict decode-only schema and its conversion to the resolved
// native MCP contract.

import (
	"encoding/json"
	"fmt"
	"time"

	"petris.dev/toby/internal/agent/resource"
	"petris.dev/toby/internal/sandbox/mount"
)

type serverSchema struct {
	Type        ServerType        `json:"type" yaml:"type"`
	Transport   Transport         `json:"transport" yaml:"transport"`
	URL         string            `json:"url" yaml:"url"`
	Headers     map[string]string `json:"headers" yaml:"headers"`
	Image       string            `json:"image" yaml:"image"`
	Command     []string          `json:"command" yaml:"command"`
	Environment map[string]string `json:"environment" yaml:"environment"`
	Endpoint    *endpointSchema   `json:"endpoint" yaml:"endpoint"`
	Mounts      []mountSchema     `json:"mounts" yaml:"mounts"`
	Scope       string            `json:"scope" yaml:"scope"`
	Network     string            `json:"network" yaml:"network"`
	IdleTimeout *durationValue    `json:"idleTimeout" yaml:"idleTimeout"`
}

type endpointSchema struct {
	Kind   EndpointKind `json:"kind" yaml:"kind"`
	Socket string       `json:"socket" yaml:"socket"`
	Path   string       `json:"path" yaml:"path"`
}

type mountSchema struct {
	Source string `json:"source" yaml:"source"`
	Target string `json:"target" yaml:"target"`
	Access string `json:"access" yaml:"access"`
	Scope  string `json:"scope" yaml:"scope"`
}

type durationValue struct {
	time.Duration
}

var _ json.Unmarshaler = (*durationValue)(nil)

func (d *durationValue) UnmarshalJSON(data []byte) error {
	var text string
	if err := json.Unmarshal(data, &text); err != nil {
		return fmt.Errorf("duration must be a string: %w", err)
	}
	if text == "" {
		return fmt.Errorf("duration must not be empty")
	}

	value, err := time.ParseDuration(text)
	if err != nil {
		return fmt.Errorf("invalid duration %q: %w", text, err)
	}
	d.Duration = value

	return nil
}

func (s serverSchema) server() Server {
	server := Server{
		Type:        s.Type,
		Transport:   s.Transport,
		URL:         s.URL,
		Headers:     cloneStringMap(s.Headers),
		Image:       s.Image,
		Command:     append([]string(nil), s.Command...),
		Environment: cloneStringMap(s.Environment),
		Scope:       resource.Scope(s.Scope),
		Network:     resource.Network(s.Network),
	}
	if s.Endpoint != nil {
		server.Endpoint = &Endpoint{
			Kind:   s.Endpoint.Kind,
			Socket: s.Endpoint.Socket,
			Path:   s.Endpoint.Path,
		}
	}
	if s.IdleTimeout != nil {
		server.IdleTimeout = s.IdleTimeout.Duration
	}
	if len(s.Mounts) > 0 {
		server.Mounts = make([]Mount, len(s.Mounts))
		for index, item := range s.Mounts {
			server.Mounts[index] = Mount{
				Source: item.Source,
				Target: item.Target,
				Access: mount.Access(item.Access),
				Scope:  resource.Scope(item.Scope),
			}
		}
	}

	return server
}
