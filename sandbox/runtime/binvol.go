package runtime

// The toby-binary volume: a content-addressed, read-only volume holding the toby
// binary, mounted into every sandbox container (home, netns, tool) so the binary no
// longer needs a per-container docker-cp and lives outside the home tree. The volume
// is keyed by the build version, or by the binary's content hash for `dev` builds so a
// rebuilt binary gets a fresh volume. It is created and populated once, then reused.

import (
	"archive/tar"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"

	"github.com/moby/moby/api/types/container"
	dmount "github.com/moby/moby/api/types/mount"
	"github.com/moby/moby/client"
	"github.com/testcontainers/testcontainers-go"

	sandboxbinary "petris.dev/toby/internal/sandbox/binary"
	"petris.dev/toby/internal/version"
)

// BinDir is where the toby-binary volume is mounted (read-only) in every container.
const BinDir = "/toby/bin"

// binVolumeImage is the throwaway image used to populate the binary volume; alpine is
// tiny and its presence is not assumed elsewhere — any image works since the helper is
// never started.
const binVolumeImage = "alpine:latest"

// EnsureBinVolume returns the name of the toby-binary volume, creating and populating
// it on first use. Reused when it already exists (idempotent, safe under concurrency —
// VolumeCreate with an existing name is a no-op and the populate is content-stable).
func EnsureBinVolume(ctx context.Context, cli *testcontainers.DockerClient) (string, error) {
	bin, err := sandboxbinary.SourceBytes()
	if err != nil {
		return "", err
	}
	name := "toby.bin." + binVolumeKey(bin)

	existing, err := cli.VolumeList(ctx, client.VolumeListOptions{Filters: client.Filters{}.Add("name", name)})
	if err != nil {
		return "", err
	}
	for _, v := range existing.Items {
		if v.Name == name {
			return name, nil
		}
	}

	if _, err := cli.VolumeCreate(ctx, client.VolumeCreateOptions{Name: name, Labels: map[string]string{"toby.bin": binVolumeKey(bin)}}); err != nil {
		return "", err
	}
	if err := populateBinVolume(ctx, cli, name, bin); err != nil {
		return "", err
	}
	return name, nil
}

// binVolumeKey is the build version for a release, or the binary's content hash for a
// `dev` build so rebuilds are distinguished.
func binVolumeKey(bin []byte) string {
	if v := version.Current; v != "" && v != "dev" {
		return sanitizeVolumeSegment(v)
	}
	sum := sha256.Sum256(bin)
	return "dev-" + hex.EncodeToString(sum[:])[:12]
}

// populateBinVolume copies the binary into the volume via a created-but-not-started
// helper container that mounts it; the copy resolves through the mount into the volume.
func populateBinVolume(ctx context.Context, cli *testcontainers.DockerClient, volume string, bin []byte) error {
	created, err := cli.ContainerCreate(ctx, client.ContainerCreateOptions{
		Config:     &container.Config{Image: binVolumeImage},
		HostConfig: &container.HostConfig{Mounts: []dmount.Mount{{Type: dmount.TypeVolume, Source: volume, Target: BinDir}}},
	})
	if err != nil {
		return fmt.Errorf("create bin-volume helper: %w", err)
	}
	defer cli.ContainerRemove(ctx, created.ID, client.ContainerRemoveOptions{Force: true})

	tarball, err := binTar(bin)
	if err != nil {
		return err
	}
	_, err = cli.CopyToContainer(ctx, created.ID, client.CopyToContainerOptions{
		DestinationPath: "/",
		Content:         tarball,
	})
	return err
}

// binTar builds a tar containing the toby binary at <BinDir>/toby, mode 0555 (RO exec).
func binTar(bin []byte) (*bytes.Buffer, error) {
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	if err := tw.WriteHeader(&tar.Header{
		Name:     "toby/bin/toby",
		Mode:     0o555,
		Size:     int64(len(bin)),
		Typeflag: tar.TypeReg,
	}); err != nil {
		return nil, err
	}
	if _, err := tw.Write(bin); err != nil {
		return nil, err
	}
	if err := tw.Close(); err != nil {
		return nil, err
	}
	return &buf, nil
}

// sanitizeVolumeSegment keeps a version usable in a docker volume name.
func sanitizeVolumeSegment(s string) string {
	out := make([]rune, 0, len(s))
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '.', r == '_', r == '-':
			out = append(out, r)
		default:
			out = append(out, '-')
		}
	}
	return string(out)
}
