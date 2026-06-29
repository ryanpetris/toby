// Package files implements file.* control methods inside the sandbox.
package files

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"

	"petris.dev/toby/internal/control"
)

const (
	defaultFileMode = 0o644
	defaultDirMode  = 0o755
)

type Service struct{}

var _ control.Capability = Service{}

func New() Service { return Service{} }

func (Service) Methods() []control.Method {
	return []control.Method{
		{Name: MethodCreate, Handle: handleCreate},
		{Name: MethodDelete, Handle: handleDelete},
		{Name: MethodMkdir, Handle: handleMkdir},
		{Name: MethodSymlink, Handle: handleSymlink},
	}
}

func handleCreate(_ context.Context, req control.RPCRequest) ([]byte, error) {
	params, err := DecodeCreateParams(req.Params)
	if err != nil {
		return control.ResponseError(req.ID, control.CodeInvalidParams, err.Error(), nil), syscall.EINVAL
	}
	if err := fileCreate(params); err != nil {
		return control.ResponseError(req.ID, control.CodeInternalError, err.Error(), nil), syscall.EIO
	}
	return control.ResponseOK(req.ID, control.EmptyResult{}), nil
}

func handleDelete(_ context.Context, req control.RPCRequest) ([]byte, error) {
	params, err := DecodeDeleteParams(req.Params)
	if err != nil {
		return control.ResponseError(req.ID, control.CodeInvalidParams, err.Error(), nil), syscall.EINVAL
	}
	if err := fileDelete(params); err != nil {
		return control.ResponseError(req.ID, control.CodeInternalError, err.Error(), nil), syscall.EIO
	}
	return control.ResponseOK(req.ID, control.EmptyResult{}), nil
}

func handleMkdir(_ context.Context, req control.RPCRequest) ([]byte, error) {
	params, err := DecodeMkdirParams(req.Params)
	if err != nil {
		return control.ResponseError(req.ID, control.CodeInvalidParams, err.Error(), nil), syscall.EINVAL
	}
	if err := fileMkdir(params); err != nil {
		return control.ResponseError(req.ID, control.CodeInternalError, err.Error(), nil), syscall.EIO
	}
	return control.ResponseOK(req.ID, control.EmptyResult{}), nil
}

func handleSymlink(_ context.Context, req control.RPCRequest) ([]byte, error) {
	params, err := DecodeSymlinkParams(req.Params)
	if err != nil {
		return control.ResponseError(req.ID, control.CodeInvalidParams, err.Error(), nil), syscall.EINVAL
	}
	if err := fileSymlink(params); err != nil {
		return control.ResponseError(req.ID, control.CodeInternalError, err.Error(), nil), syscall.EIO
	}
	return control.ResponseOK(req.ID, control.EmptyResult{}), nil
}

type fileOwner struct {
	uid int
	gid int
}

func fileCreate(params CreateParams) error {
	path, err := cleanPath(params.Path)
	if err != nil {
		return err
	}
	owner, err := cleanOwner(params.UID, params.GID)
	if err != nil {
		return err
	}
	mode := os.FileMode(params.Mode & 0o777)
	if mode == 0 {
		mode = defaultFileMode
	}
	if err := mkdirAllOwned(filepath.Dir(path), defaultDirMode, owner); err != nil {
		return err
	}
	if err := os.WriteFile(path, params.Data, mode); err != nil {
		return err
	}
	if err := chownPath(path, owner, false); err != nil {
		return err
	}
	return os.Chmod(path, mode)
}

func fileDelete(params DeleteParams) error {
	path, err := cleanPath(params.Path)
	if err != nil {
		return err
	}
	if params.Recursive {
		return os.RemoveAll(path)
	}
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}

func fileMkdir(params MkdirParams) error {
	path, err := cleanPath(params.Path)
	if err != nil {
		return err
	}
	owner, err := cleanOwner(params.UID, params.GID)
	if err != nil {
		return err
	}
	mode := os.FileMode(params.Mode & 0o777)
	if mode == 0 {
		mode = defaultDirMode
	}
	return mkdirAllOwned(path, mode, owner)
}

func fileSymlink(params SymlinkParams) error {
	path, err := cleanPath(params.Path)
	if err != nil {
		return err
	}
	owner, err := cleanOwner(params.UID, params.GID)
	if err != nil {
		return err
	}
	if strings.ContainsRune(params.Target, 0) {
		return fmt.Errorf("invalid symlink target")
	}
	if err := mkdirAllOwned(filepath.Dir(path), defaultDirMode, owner); err != nil {
		return err
	}
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if err := os.Symlink(params.Target, path); err != nil {
		return err
	}
	return chownPath(path, owner, true)
}

func cleanOwner(uid, gid int) (fileOwner, error) {
	if uid == control.HostUser || gid == control.HostGroup {
		return fileOwner{}, fmt.Errorf("unresolved host file owner")
	}
	if uid < 0 {
		return fileOwner{}, fmt.Errorf("invalid file uid")
	}
	if gid < 0 {
		return fileOwner{}, fmt.Errorf("invalid file gid")
	}
	return fileOwner{uid: uid, gid: gid}, nil
}

func mkdirAllOwned(path string, mode os.FileMode, owner fileOwner) error {
	path = filepath.Clean(path)
	if path == "." || path == string(filepath.Separator) {
		return nil
	}
	parent := filepath.Dir(path)
	if parent != path {
		if err := mkdirAllOwned(parent, defaultDirMode, owner); err != nil {
			return err
		}
	}
	if err := os.Mkdir(path, mode); err != nil {
		if errors.Is(err, os.ErrExist) {
			info, statErr := os.Stat(path)
			if statErr != nil {
				return statErr
			}
			if info.IsDir() {
				return nil
			}
		}
		return err
	}
	if err := chownPath(path, owner, false); err != nil {
		return err
	}
	return os.Chmod(path, mode)
}

func chownPath(path string, owner fileOwner, link bool) error {
	if os.Geteuid() != 0 {
		return validateExistingOwner(path, owner, link)
	}
	if link {
		return os.Lchown(path, owner.uid, owner.gid)
	}
	return os.Chown(path, owner.uid, owner.gid)
}

func validateExistingOwner(path string, owner fileOwner, link bool) error {
	uid, gid, err := statOwner(path, link)
	if err != nil {
		return err
	}
	if uid == owner.uid && gid == owner.gid {
		return nil
	}
	if owner.uid == 0 && owner.gid == 0 {
		return nil
	}
	return fmt.Errorf("cannot set owner %d:%d as non-root", owner.uid, owner.gid)
}

func statOwner(path string, link bool) (int, int, error) {
	var (
		info os.FileInfo
		err  error
	)
	if link {
		info, err = os.Lstat(path)
	} else {
		info, err = os.Stat(path)
	}
	if err != nil {
		return 0, 0, err
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return 0, 0, fmt.Errorf("file owner is unavailable")
	}
	return int(stat.Uid), int(stat.Gid), nil
}

func cleanPath(path string) (string, error) {
	path = strings.TrimSpace(path)
	if path == "" || strings.ContainsRune(path, 0) {
		return "", fmt.Errorf("invalid path")
	}
	return filepath.Clean(path), nil
}
