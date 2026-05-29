package control

import (
	"context"
	"encoding/json"
	"errors"
	"syscall"
)

type RepositoryResolver interface {
	VisibleHostPath(string) (string, error)
}

type Manager struct {
	RepositoryResolver RepositoryResolver
	ContextFiles       []ContextFile
}

func (m *Manager) Handle(ctx context.Context, data []byte) ([]byte, error) {
	req, err := DecodeRequest(data)
	if err != nil {
		var syntaxErr *json.SyntaxError
		code := CodeInvalidRequest
		if errors.As(err, &syntaxErr) {
			code = CodeParseError
		}
		return ResponseError(nil, code, err.Error(), nil), syscall.EINVAL
	}
	switch req.Method {
	case "context_files":
		return ResponseOK(req.ID, m.contextFiles()), nil
	case "git_commit":
		params, err := DecodeGitCommitParams(req.Params)
		if err != nil {
			return ResponseError(req.ID, CodeInvalidParams, err.Error(), nil), syscall.EINVAL
		}
		result, err := m.gitCommit(ctx, params.Repository, params.Message)
		if err != nil {
			return ResponseError(req.ID, rpcErrorCode(err), err.Error(), nil), errnoFor(err)
		}
		return ResponseOK(req.ID, result), nil
	case "git_fetch":
		params, err := DecodeGitRepositoryParams(req.Params)
		if err != nil {
			return ResponseError(req.ID, CodeInvalidParams, err.Error(), nil), syscall.EINVAL
		}
		result, err := m.gitFetch(ctx, params.Repository)
		if err != nil {
			return ResponseError(req.ID, rpcErrorCode(err), err.Error(), nil), errnoFor(err)
		}
		return ResponseOK(req.ID, result), nil
	case "git_push":
		params, err := DecodeGitPushParams(req.Params)
		if err != nil {
			return ResponseError(req.ID, CodeInvalidParams, err.Error(), nil), syscall.EINVAL
		}
		result, err := m.gitPush(ctx, params.Repository, params.Branch, params.Origin)
		if err != nil {
			return ResponseError(req.ID, rpcErrorCode(err), err.Error(), nil), errnoFor(err)
		}
		return ResponseOK(req.ID, result), nil
	default:
		return ResponseError(req.ID, CodeMethodNotFound, "method not found: "+req.Method, nil), syscall.ENOSYS
	}
}

func (m *Manager) contextFiles() ContextFilesResult {
	files := make([]ContextFile, 0, len(m.ContextFiles))
	for _, file := range m.ContextFiles {
		files = append(files, ContextFile{Path: file.Path, Mode: file.Mode, Data: append([]byte(nil), file.Data...)})
	}
	return ContextFilesResult{Files: files}
}

var ErrProjectNotVisible = errors.New("repository is not visible in the sandbox")

func errnoFor(err error) error {
	if errors.Is(err, ErrProjectNotVisible) {
		return syscall.EACCES
	}
	var errno syscall.Errno
	if errors.As(err, &errno) {
		return errno
	}
	return syscall.EIO
}

func rpcErrorCode(err error) int {
	if errors.Is(err, ErrProjectNotVisible) {
		return CodeProjectNotVisible
	}
	if errors.Is(err, syscall.EINVAL) {
		return CodeInvalidParams
	}
	if errors.Is(err, syscall.ENOSYS) {
		return CodeInternalError
	}
	return CodeInternalError
}
