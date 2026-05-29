package sandboxmanager

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"

	"petris.dev/toby/internal/control"

	"go.uber.org/fx"
)

type FileServiceResult struct {
	fx.Out

	Service Service `group:"toby.sandbox.manager.services"`
}

type FileService struct{}

func NewFileService() FileServiceResult {
	return FileServiceResult{Service: FileService{}}
}

func (FileService) Commands() []Command {
	return []Command{
		CommandFunc{Name: control.MethodFileCreate, Run: handleFileCreate},
		CommandFunc{Name: control.MethodFileDelete, Run: handleFileDelete},
		CommandFunc{Name: control.MethodFileMkdir, Run: handleFileMkdir},
		CommandFunc{Name: control.MethodFileSymlink, Run: handleFileSymlink},
	}
}

func handleFileCreate(_ context.Context, _ *Runtime, req control.RPCRequest) ([]byte, error) {
	params, err := control.DecodeFileCreateParams(req.Params)
	if err != nil {
		return control.ResponseError(req.ID, control.CodeInvalidParams, err.Error(), nil), syscall.EINVAL
	}
	if err := fileCreate(params); err != nil {
		return control.ResponseError(req.ID, control.CodeInternalError, err.Error(), nil), syscall.EIO
	}
	return control.ResponseOK(req.ID, control.EmptyResult{}), nil
}

func handleFileDelete(_ context.Context, _ *Runtime, req control.RPCRequest) ([]byte, error) {
	params, err := control.DecodeFileDeleteParams(req.Params)
	if err != nil {
		return control.ResponseError(req.ID, control.CodeInvalidParams, err.Error(), nil), syscall.EINVAL
	}
	if err := fileDelete(params); err != nil {
		return control.ResponseError(req.ID, control.CodeInternalError, err.Error(), nil), syscall.EIO
	}
	return control.ResponseOK(req.ID, control.EmptyResult{}), nil
}

func handleFileMkdir(_ context.Context, _ *Runtime, req control.RPCRequest) ([]byte, error) {
	params, err := control.DecodeFileMkdirParams(req.Params)
	if err != nil {
		return control.ResponseError(req.ID, control.CodeInvalidParams, err.Error(), nil), syscall.EINVAL
	}
	if err := fileMkdir(params); err != nil {
		return control.ResponseError(req.ID, control.CodeInternalError, err.Error(), nil), syscall.EIO
	}
	return control.ResponseOK(req.ID, control.EmptyResult{}), nil
}

func handleFileSymlink(_ context.Context, _ *Runtime, req control.RPCRequest) ([]byte, error) {
	params, err := control.DecodeFileSymlinkParams(req.Params)
	if err != nil {
		return control.ResponseError(req.ID, control.CodeInvalidParams, err.Error(), nil), syscall.EINVAL
	}
	if err := fileSymlink(params); err != nil {
		return control.ResponseError(req.ID, control.CodeInternalError, err.Error(), nil), syscall.EIO
	}
	return control.ResponseOK(req.ID, control.EmptyResult{}), nil
}

func fileCreate(params control.FileCreateParams) error {
	path, err := cleanPath(params.Path)
	if err != nil {
		return err
	}
	mode := os.FileMode(params.Mode & 0o777)
	if mode == 0 {
		mode = 0o400
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	if err := os.WriteFile(path, params.Data, mode); err != nil {
		return err
	}
	return os.Chmod(path, mode)
}

func fileDelete(params control.FileDeleteParams) error {
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

func fileMkdir(params control.FileMkdirParams) error {
	path, err := cleanPath(params.Path)
	if err != nil {
		return err
	}
	mode := os.FileMode(params.Mode & 0o777)
	if mode == 0 {
		mode = 0o700
	}
	return os.MkdirAll(path, mode)
}

func fileSymlink(params control.FileSymlinkParams) error {
	path, err := cleanPath(params.Path)
	if err != nil {
		return err
	}
	if strings.ContainsRune(params.Target, 0) {
		return fmt.Errorf("invalid symlink target")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return os.Symlink(params.Target, path)
}

func cleanPath(path string) (string, error) {
	path = strings.TrimSpace(path)
	if path == "" || strings.ContainsRune(path, 0) {
		return "", fmt.Errorf("invalid path")
	}
	return filepath.Clean(path), nil
}
