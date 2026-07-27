// Package git is the host-side action capability for running Git commands in
// repositories that are visible inside the sandbox. It resolves a sandbox-relative
// repository name to its host path and runs Git there with the host's credentials.
package git

import (
	"context"
	"fmt"
	"os"
	"strings"
	"sync"
	"syscall"

	"petris.dev/toby/internal/approval"
	"petris.dev/toby/internal/diagnostic"
	"petris.dev/toby/internal/hostaction"
	"petris.dev/toby/internal/permission"
)

// RepositoryResolver opens the exact host directory authorized by a
// sandbox-visible repository name. The caller owns the returned descriptor.
type RepositoryResolver interface {
	// OpenVisibleHostDirectory opens an authorized sandbox-visible repository.
	OpenVisibleHostDirectory(string) (*os.File, error)
}

// Approver decides whether an action may proceed, prompting the user when needed.
type Approver interface {
	// Request resolves one host-action approval decision.
	Request(ctx context.Context, req approval.Request) (permission.Decision, error)
}

var _ hostaction.Capability = (*Service)(nil)

// Service handles the git.* methods. The resolver and approver are injected after
// construction (once the sandbox is prepared) via SetResolver and SetApprover.
type Service struct {
	mu       sync.RWMutex
	resolver RepositoryResolver
	approver Approver
	runner   CommandRunner
	logger   *diagnostic.Logger
}

// New creates a Git capability with fail-closed host-command supervision and
// no repository resolver; call SetResolver before use.
func New(diagnostics *diagnostic.Service) *Service {
	logger := diagnostics.Logger("hostaction.git")
	return &Service{
		runner: newCommandRunner(logger),
		logger: logger,
	}
}

// SetResolver installs the repository resolver used to locate visible repos.
func (s *Service) SetResolver(resolver RepositoryResolver) {
	s.mu.Lock()
	s.resolver = resolver
	s.mu.Unlock()
}

// SetApprover installs the approver consulted before each git operation runs.
func (s *Service) SetApprover(approver Approver) {
	s.mu.Lock()
	s.approver = approver
	s.mu.Unlock()
}

// approve consults the approver for an action, supplying the action's default rule; it
// returns ErrPermissionDenied when the action is refused, or nil when allowed (or when
// no approver is wired).
func (s *Service) approve(ctx context.Context, action, name, message string, def permission.Rule) error {
	s.mu.RLock()
	approver := s.approver
	s.mu.RUnlock()
	if approver == nil {
		return nil
	}
	decision, err := approver.Request(ctx, approval.Request{Action: action, Name: name, Message: message, Default: def})
	if err != nil {
		return err
	}
	if decision != permission.Allow {
		return ErrPermissionDenied
	}
	return nil
}

// Methods registers the git.* handlers into the host router.
func (s *Service) Methods() []hostaction.Method {
	return []hostaction.Method{
		{Name: MethodCommit, Handle: s.handleGitCommit},
		{Name: MethodFetch, Handle: s.handleGitFetch},
		{Name: MethodPush, Handle: s.handleGitPush},
		{Name: MethodRebase, Handle: s.handleGitRebase},
		{Name: MethodTag, Handle: s.handleGitTag},
	}
}

func (s *Service) handleGitCommit(ctx context.Context, req hostaction.RPCRequest) ([]byte, error) {
	params, err := DecodeCommitParams(req.Params)
	if err != nil {
		return hostaction.ResponseError(req.ID, hostaction.CodeInvalidParams, err.Error(), nil), syscall.EINVAL
	}
	if err := s.approve(ctx, MethodCommit, "Git commit", fmt.Sprintf("Commit in %s", params.Repository), permission.RuleAllow); err != nil {
		return hostaction.ResponseError(req.ID, rpcErrorCode(err), err.Error(), nil), errnoFor(err)
	}
	result, err := s.gitCommit(ctx, params.Repository, params.Message, params.Amend)
	if err != nil {
		return hostaction.ResponseError(req.ID, rpcErrorCode(err), err.Error(), nil), errnoFor(err)
	}
	return hostaction.ResponseOK(req.ID, result), nil
}

func (s *Service) handleGitFetch(ctx context.Context, req hostaction.RPCRequest) ([]byte, error) {
	params, err := DecodeRepositoryParams(req.Params)
	if err != nil {
		return hostaction.ResponseError(req.ID, hostaction.CodeInvalidParams, err.Error(), nil), syscall.EINVAL
	}
	if err := s.approve(ctx, MethodFetch, "Git fetch", fmt.Sprintf("Fetch in %s", params.Repository), permission.RuleAllow); err != nil {
		return hostaction.ResponseError(req.ID, rpcErrorCode(err), err.Error(), nil), errnoFor(err)
	}
	result, err := s.gitFetch(ctx, params.Repository)
	if err != nil {
		return hostaction.ResponseError(req.ID, rpcErrorCode(err), err.Error(), nil), errnoFor(err)
	}
	return hostaction.ResponseOK(req.ID, result), nil
}

func (s *Service) handleGitPush(ctx context.Context, req hostaction.RPCRequest) ([]byte, error) {
	params, err := DecodePushParams(req.Params)
	if err != nil {
		return hostaction.ResponseError(req.ID, hostaction.CodeInvalidParams, err.Error(), nil), syscall.EINVAL
	}
	origin := params.Origin
	if strings.TrimSpace(origin) == "" {
		origin = "origin"
	}
	message := fmt.Sprintf("Push %s to %s in %s", params.Branch, origin, params.Repository)
	if err := s.approve(ctx, MethodPush, "Git push", message, permission.RuleAsk); err != nil {
		return hostaction.ResponseError(req.ID, rpcErrorCode(err), err.Error(), nil), errnoFor(err)
	}
	result, err := s.gitPush(ctx, params.Repository, params.Branch, params.Origin, params.Tags)
	if err != nil {
		return hostaction.ResponseError(req.ID, rpcErrorCode(err), err.Error(), nil), errnoFor(err)
	}
	return hostaction.ResponseOK(req.ID, result), nil
}

func (s *Service) handleGitRebase(ctx context.Context, req hostaction.RPCRequest) ([]byte, error) {
	params, err := DecodeRebaseParams(req.Params)
	if err != nil {
		return hostaction.ResponseError(req.ID, hostaction.CodeInvalidParams, err.Error(), nil), syscall.EINVAL
	}
	if err := s.approve(ctx, MethodRebase, "Git rebase", fmt.Sprintf("Rebase in %s", params.Repository), permission.RuleAllow); err != nil {
		return hostaction.ResponseError(req.ID, rpcErrorCode(err), err.Error(), nil), errnoFor(err)
	}
	result, err := s.gitRebase(ctx, params.Repository, params.Base, params.Continue, params.Abort)
	if err != nil {
		return hostaction.ResponseError(req.ID, rpcErrorCode(err), err.Error(), nil), errnoFor(err)
	}
	return hostaction.ResponseOK(req.ID, result), nil
}

func (s *Service) handleGitTag(ctx context.Context, req hostaction.RPCRequest) ([]byte, error) {
	params, err := DecodeTagParams(req.Params)
	if err != nil {
		return hostaction.ResponseError(req.ID, hostaction.CodeInvalidParams, err.Error(), nil), syscall.EINVAL
	}
	if err := s.approve(ctx, MethodTag, "Git tag", fmt.Sprintf("Tag %s in %s", params.Tag, params.Repository), permission.RuleAllow); err != nil {
		return hostaction.ResponseError(req.ID, rpcErrorCode(err), err.Error(), nil), errnoFor(err)
	}
	result, err := s.gitTag(ctx, params.Repository, params.Tag, params.Message, params.Target)
	if err != nil {
		return hostaction.ResponseError(req.ID, rpcErrorCode(err), err.Error(), nil), errnoFor(err)
	}
	return hostaction.ResponseOK(req.ID, result), nil
}

func (s *Service) gitCommit(ctx context.Context, repository, message string, amend bool) (Result, error) {
	repository, err := validateRepositoryName(repository)
	if err != nil {
		return Result{}, err
	}
	if strings.TrimSpace(message) == "" || strings.ContainsRune(message, 0) {
		return Result{}, syscall.EINVAL
	}
	args := []string{"commit"}
	if amend {
		args = append(args, "--amend")
	}
	args = append(args, "-m", message)
	return s.runVisibleGit(ctx, repository, args)
}

func (s *Service) gitFetch(ctx context.Context, repository string) (Result, error) {
	repository, err := validateRepositoryName(repository)
	if err != nil {
		return Result{}, err
	}
	return s.runVisibleGit(ctx, repository, []string{"fetch"})
}

func (s *Service) gitPush(ctx context.Context, repository, branch, origin string, tags bool) (Result, error) {
	repository, err := validateRepositoryName(repository)
	if err != nil {
		return Result{}, err
	}
	branch, err = validateGitArgument(branch)
	if err != nil {
		return Result{}, err
	}
	if strings.TrimSpace(origin) == "" {
		origin = "origin"
	} else {
		origin, err = validateGitArgument(origin)
		if err != nil {
			return Result{}, err
		}
	}
	args := []string{"push"}
	if tags {
		args = append(args, "--tags")
	}
	args = append(args, origin, branch)
	return s.runVisibleGit(ctx, repository, args)
}

func (s *Service) gitRebase(ctx context.Context, repository, base string, continueRebase, abort bool) (Result, error) {
	repository, err := validateRepositoryName(repository)
	if err != nil {
		return Result{}, err
	}
	if continueRebase {
		return s.runVisibleGit(ctx, repository, []string{"-c", "core.editor=true", "rebase", "--continue"})
	}
	if abort {
		return s.runVisibleGit(ctx, repository, []string{"rebase", "--abort"})
	}
	base, err = validateGitArgument(base)
	if err != nil {
		return Result{}, err
	}
	return s.runVisibleGit(ctx, repository, []string{"rebase", base})
}

func (s *Service) gitTag(ctx context.Context, repository, tag, message, target string) (Result, error) {
	repository, err := validateRepositoryName(repository)
	if err != nil {
		return Result{}, err
	}
	tag, err = validateGitArgument(tag)
	if err != nil {
		return Result{}, err
	}
	if strings.TrimSpace(message) == "" || strings.ContainsRune(message, 0) {
		return Result{}, syscall.EINVAL
	}
	args := []string{"tag", "-a", tag, "-m", message}
	if strings.TrimSpace(target) != "" {
		target, err = validateGitArgument(target)
		if err != nil {
			return Result{}, err
		}
		args = append(args, target)
	}
	return s.runVisibleGit(ctx, repository, args)
}

func (s *Service) runVisibleGit(
	ctx context.Context,
	repository string,
	args []string,
) (result Result, returnErr error) {
	s.mu.RLock()
	resolver := s.resolver
	runner := s.runner
	s.mu.RUnlock()
	if resolver == nil || runner == nil {
		return Result{}, syscall.ENOSYS
	}
	repositoryDirectory, err := resolver.OpenVisibleHostDirectory(repository)
	if err != nil {
		return Result{}, wrapProjectNotVisible(err)
	}
	if repositoryDirectory == nil {
		return Result{}, wrapProjectNotVisible(
			fmt.Errorf("repository resolver returned a nil directory"),
		)
	}
	defer func() {
		s.logger.DebugError(
			"close host Git repository directory",
			repositoryDirectory.Close(),
			"repository",
			repository,
		)
	}()
	info, err := repositoryDirectory.Stat()
	if err != nil {
		return Result{}, wrapProjectNotVisible(err)
	}
	if !info.IsDir() {
		return Result{}, wrapProjectNotVisible(
			fmt.Errorf("repository resolver returned a non-directory"),
		)
	}

	return runGit(
		ctx,
		runner,
		repository,
		repositoryDirectory,
		args,
		s.logger,
	), nil
}
