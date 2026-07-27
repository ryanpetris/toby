package agent

// Verifies that each agent process builder uses an independent ephemeral key.

import (
	"testing"

	"petris.dev/toby/internal/agent/resource"
)

func TestResourceBuildersSeparateSecretFingerprints(t *testing.T) {
	first, err := newResourceBuilder()
	if err != nil {
		t.Fatal(err)
	}
	second, err := newResourceBuilder()
	if err != nil {
		t.Fatal(err)
	}

	spec := resource.Spec{
		Kind:           resource.KindMCPHTTP,
		Transport:      resource.TransportHTTP,
		ManifestDigest: "sha256:aa",
		RootFSDigest:   "sha256:bb",
		Argv:           []string{"/bin/server"},
		Workdir:        "/",
		Identity:       resource.Identity{UID: 1000, GID: 1000},
		Environment: []resource.EnvironmentVariable{{
			Name:      "TOKEN",
			Value:     "low-entropy-secret",
			Sensitive: true,
		}},
		Endpoint: resource.Endpoint{
			Kind:   resource.EndpointUnix,
			Socket: "/run/server.sock",
			Path:   "/mcp",
		},
		Network:         resource.NetworkHost,
		BridgeVersion:   "1",
		ProtocolVersion: "1",
		RequestedScope:  resource.ScopeUser,
		RunAuthority:    resource.RunAuthorityAbsent,
	}
	firstKey, err := first.Build(spec)
	if err != nil {
		t.Fatal(err)
	}
	secondKey, err := second.Build(spec)
	if err != nil {
		t.Fatal(err)
	}
	if firstKey == secondKey {
		t.Fatal("independent agent HMAC keys produced the same identity")
	}
}
