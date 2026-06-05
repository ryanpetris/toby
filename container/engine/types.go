package engine

// Data types recorded for tracked containers: how a container is classified for
// introspection (Kind), the metadata stored per container (Meta, record), and
// the sanitized snapshot exposed to introspection resources (ContainerInfo).

import (
	"time"

	"github.com/testcontainers/testcontainers-go"
)

// Kind classifies a tracked container for introspection.
type Kind string

const (
	KindSandbox    Kind = "sandbox"
	KindMCPSidecar Kind = "mcp-sidecar"
)

// Meta is the introspection metadata recorded for a tracked container.
type Meta struct {
	Label   string
	Kind    Kind
	Phase   string
	Image   string
	Network string
}

// ContainerInfo is a sanitized snapshot of a tracked container. It never
// contains environment variables, argv, or secrets.
type ContainerInfo struct {
	ID      string
	Label   string
	Kind    Kind
	Phase   string
	Image   string
	Network string
}

type record struct {
	ctr       testcontainers.Container
	meta      Meta
	createdAt time.Time
}
