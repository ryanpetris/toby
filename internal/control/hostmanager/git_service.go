package hostmanager

import (
	"context"
	"strings"
	"syscall"

	"petris.dev/toby/internal/control"

	"go.uber.org/fx"
)

type GitServiceResult struct {
	fx.Out

	Service Service `group:"toby.manager.services"`
}

type GitService struct{}

func NewGitService() GitServiceResult {
	return GitServiceResult{Service: GitService{}}
}

func (GitService) Commands() []Command {
	return []Command{
		CommandFunc{Name: control.MethodGitCommit, Run: handleGitCommit},
		CommandFunc{Name: control.MethodGitFetch, Run: handleGitFetch},
		CommandFunc{Name: control.MethodGitPush, Run: handleGitPush},
		CommandFunc{Name: control.MethodGitRebase, Run: handleGitRebase},
		CommandFunc{Name: control.MethodGitTag, Run: handleGitTag},
	}
}

func handleGitCommit(ctx context.Context, runtime *Runtime, req control.RPCRequest) ([]byte, error) {
	params, err := control.DecodeGitCommitParams(req.Params)
	if err != nil {
		return control.ResponseError(req.ID, control.CodeInvalidParams, err.Error(), nil), syscall.EINVAL
	}
	result, err := runtime.gitCommit(ctx, params.Repository, params.Message, params.Amend)
	if err != nil {
		return control.ResponseError(req.ID, rpcErrorCode(err), err.Error(), nil), errnoFor(err)
	}
	return control.ResponseOK(req.ID, result), nil
}

func handleGitFetch(ctx context.Context, runtime *Runtime, req control.RPCRequest) ([]byte, error) {
	params, err := control.DecodeGitRepositoryParams(req.Params)
	if err != nil {
		return control.ResponseError(req.ID, control.CodeInvalidParams, err.Error(), nil), syscall.EINVAL
	}
	result, err := runtime.gitFetch(ctx, params.Repository)
	if err != nil {
		return control.ResponseError(req.ID, rpcErrorCode(err), err.Error(), nil), errnoFor(err)
	}
	return control.ResponseOK(req.ID, result), nil
}

func handleGitPush(ctx context.Context, runtime *Runtime, req control.RPCRequest) ([]byte, error) {
	params, err := control.DecodeGitPushParams(req.Params)
	if err != nil {
		return control.ResponseError(req.ID, control.CodeInvalidParams, err.Error(), nil), syscall.EINVAL
	}
	result, err := runtime.gitPush(ctx, params.Repository, params.Branch, params.Origin, params.Tags)
	if err != nil {
		return control.ResponseError(req.ID, rpcErrorCode(err), err.Error(), nil), errnoFor(err)
	}
	return control.ResponseOK(req.ID, result), nil
}

func handleGitRebase(ctx context.Context, runtime *Runtime, req control.RPCRequest) ([]byte, error) {
	params, err := control.DecodeGitRebaseParams(req.Params)
	if err != nil {
		return control.ResponseError(req.ID, control.CodeInvalidParams, err.Error(), nil), syscall.EINVAL
	}
	result, err := runtime.gitRebase(ctx, params.Repository, params.Base, params.Continue, params.Abort)
	if err != nil {
		return control.ResponseError(req.ID, rpcErrorCode(err), err.Error(), nil), errnoFor(err)
	}
	return control.ResponseOK(req.ID, result), nil
}

func handleGitTag(ctx context.Context, runtime *Runtime, req control.RPCRequest) ([]byte, error) {
	params, err := control.DecodeGitTagParams(req.Params)
	if err != nil {
		return control.ResponseError(req.ID, control.CodeInvalidParams, err.Error(), nil), syscall.EINVAL
	}
	result, err := runtime.gitTag(ctx, params.Repository, params.Tag, params.Message, params.Target)
	if err != nil {
		return control.ResponseError(req.ID, rpcErrorCode(err), err.Error(), nil), errnoFor(err)
	}
	return control.ResponseOK(req.ID, result), nil
}

func (r *Runtime) gitCommit(ctx context.Context, repository, message string, amend bool) (control.GitResult, error) {
	repository, err := validateRepositoryName(repository)
	if err != nil {
		return control.GitResult{}, err
	}
	if strings.TrimSpace(message) == "" || strings.ContainsRune(message, 0) {
		return control.GitResult{}, syscall.EINVAL
	}
	args := []string{"commit"}
	if amend {
		args = append(args, "--amend")
	}
	args = append(args, "-m", message)
	return r.runVisibleGit(ctx, repository, args)
}

func (r *Runtime) gitFetch(ctx context.Context, repository string) (control.GitResult, error) {
	repository, err := validateRepositoryName(repository)
	if err != nil {
		return control.GitResult{}, err
	}
	return r.runVisibleGit(ctx, repository, []string{"fetch"})
}

func (r *Runtime) gitPush(ctx context.Context, repository, branch, origin string, tags bool) (control.GitResult, error) {
	repository, err := validateRepositoryName(repository)
	if err != nil {
		return control.GitResult{}, err
	}
	branch, err = validateGitArgument(branch)
	if err != nil {
		return control.GitResult{}, err
	}
	if strings.TrimSpace(origin) == "" {
		origin = "origin"
	} else {
		origin, err = validateGitArgument(origin)
		if err != nil {
			return control.GitResult{}, err
		}
	}
	args := []string{"push"}
	if tags {
		args = append(args, "--tags")
	}
	args = append(args, origin, branch)
	return r.runVisibleGit(ctx, repository, args)
}

func (r *Runtime) gitRebase(ctx context.Context, repository, base string, continueRebase, abort bool) (control.GitResult, error) {
	repository, err := validateRepositoryName(repository)
	if err != nil {
		return control.GitResult{}, err
	}
	if continueRebase {
		return r.runVisibleGit(ctx, repository, []string{"-c", "core.editor=true", "rebase", "--continue"})
	}
	if abort {
		return r.runVisibleGit(ctx, repository, []string{"rebase", "--abort"})
	}
	base, err = validateGitArgument(base)
	if err != nil {
		return control.GitResult{}, err
	}
	return r.runVisibleGit(ctx, repository, []string{"rebase", base})
}

func (r *Runtime) gitTag(ctx context.Context, repository, tag, message, target string) (control.GitResult, error) {
	repository, err := validateRepositoryName(repository)
	if err != nil {
		return control.GitResult{}, err
	}
	tag, err = validateGitArgument(tag)
	if err != nil {
		return control.GitResult{}, err
	}
	if strings.TrimSpace(message) == "" || strings.ContainsRune(message, 0) {
		return control.GitResult{}, syscall.EINVAL
	}
	args := []string{"tag", "-a", tag, "-m", message}
	if strings.TrimSpace(target) != "" {
		target, err = validateGitArgument(target)
		if err != nil {
			return control.GitResult{}, err
		}
		args = append(args, target)
	}
	return r.runVisibleGit(ctx, repository, args)
}

func (r *Runtime) runVisibleGit(ctx context.Context, repository string, args []string) (control.GitResult, error) {
	if r.Manager.RepositoryResolver == nil {
		return control.GitResult{}, syscall.ENOSYS
	}
	repoPath, err := r.Manager.RepositoryResolver.VisibleHostPath(repository)
	if err != nil {
		return control.GitResult{}, wrapProjectNotVisible(err)
	}
	return runGit(ctx, repository, repoPath, args), nil
}
