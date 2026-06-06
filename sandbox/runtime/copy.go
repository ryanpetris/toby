package runtime

// File provisioning into the Run container via docker cp (CopyToContainer with an
// in-memory tar). Tar headers carry mode + uid/gid, so the *Owned variants map
// directly; CopyUIDGID makes the daemon honor them. Deletes have no cp equivalent
// and run as a root docker exec.

import (
	"archive/tar"
	"bytes"
	"context"
	"fmt"
	"os"
	"path"
	"strings"

	"petris.dev/toby/internal/control"

	"github.com/moby/moby/client"
)

// WriteFile creates a regular file in the Run container.
func (s *instance) WriteFile(ctx context.Context, p string, data []byte, mode uint32, uid, gid int) error {
	u, g := resolveOwner(uid, gid)
	return s.copyToContainer(ctx, []copyEntry{{path: p, mode: mode, uid: u, gid: g, typ: tar.TypeReg, data: data}})
}

// MakeDir creates a directory in the Run container.
func (s *instance) MakeDir(ctx context.Context, p string, mode uint32, uid, gid int) error {
	u, g := resolveOwner(uid, gid)
	return s.copyToContainer(ctx, []copyEntry{{path: p, mode: mode, uid: u, gid: g, typ: tar.TypeDir}})
}

// MakeSymlink creates a symlink in the Run container.
func (s *instance) MakeSymlink(ctx context.Context, p, target string, uid, gid int) error {
	u, g := resolveOwner(uid, gid)
	return s.copyToContainer(ctx, []copyEntry{{path: p, mode: 0o777, uid: u, gid: g, typ: tar.TypeSymlink, link: target}})
}

// resolveOwner maps the host-user/group sentinels to the host's real uid/gid and
// clamps invalid negatives to root.
func resolveOwner(uid, gid int) (int, int) {
	return resolveID(uid, os.Getuid()), resolveID(gid, os.Getgid())
}

func resolveID(v, host int) int {
	switch {
	case v == control.HostUser: // HostUser == HostGroup
		return host
	case v < 0:
		return 0
	default:
		return v
	}
}

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

// copyToContainer writes entries into the Run container in one cp call. Missing
// parent directories are created by the daemon's untar (root-owned, like the old
// mkdir -p path).
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

// DeletePath removes a path in the Run container via a root exec. recursive maps
// to rm -rf; otherwise rm -f (which fails on a directory, matching os.Remove).
func (s *instance) DeletePath(ctx context.Context, target string, recursive bool) error {
	if err := validateDeletePath(target); err != nil {
		return err
	}
	flag := "-f"
	if recursive {
		flag = "-rf"
	}
	code, err := s.Exec(ctx, ExecSpec{
		Argv:       []string{"rm", flag, "--", target},
		User:       "0:0",
		HideOutput: true,
	})
	if err != nil {
		return err
	}
	if code != 0 {
		return fmt.Errorf("rm %s exited with code %d", target, code)
	}
	return nil
}

// validateDeletePath guards the root exec against traversal and obviously unsafe
// targets.
func validateDeletePath(target string) error {
	if !path.IsAbs(target) {
		return fmt.Errorf("delete path must be absolute: %q", target)
	}
	clean := path.Clean(target)
	if clean == "/" {
		return fmt.Errorf("refusing to delete %q", target)
	}
	for _, part := range strings.Split(clean, "/") {
		if part == ".." {
			return fmt.Errorf("delete path must not contain ..: %q", target)
		}
	}
	return nil
}
