package runtime

// Bootstrap file delivery into the Run container via docker cp. This is used to
// copy the Toby binary before the in-sandbox manager exists; live file operations
// go through the manager control session instead.

import (
	"archive/tar"
	"bytes"
	"context"
	"fmt"
	"strings"

	"github.com/moby/moby/client"
)

// copyEntry is one filesystem object to materialize in the container.
type copyEntry struct {
	path string // absolute container path
	mode uint32
	uid  int
	gid  int
	typ  byte   // tar.TypeReg, tar.TypeDir, or tar.TypeSymlink
	data []byte // for regular files
	link string // for symlinks
}

// copyToContainer writes bootstrap entries into the Run container in one cp call.
func (s *instance) copyToContainer(ctx context.Context, entries []copyEntry) error {
	cli, err := s.containers.Client(ctx)
	if err != nil {
		return err
	}
	id := s.runContainerID()
	if id == "" {
		return fmt.Errorf("run container is not started")
	}

	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	for _, e := range entries {
		name := strings.TrimPrefix(e.path, "/")
		if name == "" {
			return fmt.Errorf("invalid copy path %q", e.path)
		}
		hdr := &tar.Header{
			Name:     name,
			Mode:     int64(e.mode),
			Uid:      e.uid,
			Gid:      e.gid,
			Typeflag: e.typ,
		}
		switch e.typ {
		case tar.TypeReg:
			hdr.Size = int64(len(e.data))
		case tar.TypeDir:
			hdr.Name = name + "/"
		case tar.TypeSymlink:
			hdr.Linkname = e.link
		default:
			return fmt.Errorf("unsupported copy entry type %d", e.typ)
		}
		if err := tw.WriteHeader(hdr); err != nil {
			return err
		}
		if e.typ == tar.TypeReg && len(e.data) > 0 {
			if _, err := tw.Write(e.data); err != nil {
				return err
			}
		}
	}
	if err := tw.Close(); err != nil {
		return err
	}

	_, err = cli.CopyToContainer(ctx, id, client.CopyToContainerOptions{
		DestinationPath: "/",
		Content:         &buf,
		CopyUIDGID:      true,
	})
	return err
}
