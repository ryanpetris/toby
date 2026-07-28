package caddy

// Defines the Caddy OCI image and sandbox execution contract that participate
// in reusable process identity.

import (
	distref "github.com/distribution/reference"
)

const (
	// DefaultImage is the official Docker Hub Caddy image used by the provider
	// gateway.
	DefaultImage = "docker.io/library/caddy:latest"

	defaultBinary         = "/usr/bin/caddy"
	bridgeVersion         = "1"
	adminProtocolVersion  = "1"
	defaultServiceWorkdir = "/run/toby/service"
	defaultAdminSocket    = "/run/toby/service/admin.sock"
	defaultDataSocket     = "/run/toby/service/data.sock"
	defaultAuthSocket     = "/run/toby/auth.sock"
)

var defaultCommand = []string{
	defaultBinary,
	"run",
	"--config",
	"-",
}

func normalizeImage(value string) (string, error) {
	if value == "" {
		value = DefaultImage
	}

	reference, err := distref.ParseNormalizedNamed(value)
	if err != nil {
		return "", err
	}

	return distref.TagNameOnly(reference).String(), nil
}
