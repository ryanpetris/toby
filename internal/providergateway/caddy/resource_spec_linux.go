//go:build linux

package caddy

// Derives the stable Caddy resource identity from pinned capabilities.

import (
	"fmt"
	"os"
	"syscall"

	"petris.dev/toby/internal/agent/resource"
	"petris.dev/toby/internal/oci"
)

func (p *Pool) resourceSpec(
	prepared *oci.Prepared,
	auth *os.File,
	resolver *os.File,
) (resource.Spec, error) {
	rootfs := prepared.Spec()
	if rootfs.Manifest.Digest.String() == "" ||
		rootfs.Config.Digest.String() == "" {
		return resource.Spec{}, fmt.Errorf(
			"caddy OCI identity is incomplete",
		)
	}

	authIdentity, err := sourceIdentity(auth)
	if err != nil {
		return resource.Spec{}, err
	}
	resolverIdentity, err := sourceIdentity(resolver)
	if err != nil {
		return resource.Spec{}, err
	}

	return resource.Spec{
		Kind:           resource.KindCaddy,
		Transport:      resource.TransportHTTP,
		ManifestDigest: rootfs.Manifest.Digest.String(),
		RootFSDigest:   rootfs.Config.Digest.String(),
		Argv:           append([]string(nil), defaultCommand...),
		Workdir:        defaultServiceWorkdir,
		Identity: resource.Identity{
			UID: os.Geteuid(),
			GID: os.Getegid(),
		},
		Environment: []resource.EnvironmentVariable{
			{Name: "XDG_CONFIG_HOME", Value: "/tmp/config"},
			{Name: "XDG_DATA_HOME", Value: "/tmp/data"},
		},
		Endpoint: resource.Endpoint{
			Kind:   resource.EndpointUnix,
			Socket: defaultDataSocket,
			Path:   "/",
		},
		Mounts: []resource.Mount{
			{
				Source:         p.authPath,
				SourceIdentity: authIdentity,
				Target:         defaultAuthSocket,
				Access:         "read_only",
				Scope:          resource.ScopeUser,
			},
			{
				Source:         defaultResolverSource,
				SourceIdentity: resolverIdentity,
				Target:         defaultResolverTarget,
				Access:         "read_only",
				Scope:          resource.ScopeUser,
			},
		},
		Network:         resource.NetworkHost,
		IdleTimeout:     p.options.IdleTimeout,
		BridgeVersion:   bridgeVersion,
		ProtocolVersion: adminProtocolVersion,
		RequestedScope:  resource.ScopeUser,
		RunAuthority:    resource.RunAuthorityAbsent,
	}, nil
}

func sourceIdentity(file *os.File) (resource.MountSourceIdentity, error) {
	if file == nil {
		return resource.MountSourceIdentity{}, fmt.Errorf(
			"caddy source capability is nil",
		)
	}
	info, err := file.Stat()
	if err != nil {
		return resource.MountSourceIdentity{}, fmt.Errorf(
			"inspect Caddy source capability",
		)
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || stat == nil {
		return resource.MountSourceIdentity{}, fmt.Errorf(
			"caddy source capability identity is unavailable",
		)
	}

	return resource.MountSourceIdentity{
		Device:   uint64(stat.Dev),
		Inode:    stat.Ino,
		FileType: uint32(info.Mode().Type()),
	}, nil
}
