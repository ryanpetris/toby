package git

// The git method contract: method names, typed request builders, and param/result
// decoders. Senders (the host-side MCP server) and the handler in this package
// share these so the wire shape lives in exactly one place.

import (
	"encoding/json"
	"errors"

	"petris.dev/toby/internal/hostaction"
)

// Host-action method names for the Git capability.
const (
	// MethodCommit is the Git commit RPC method.
	MethodCommit = "git.commit"
	// MethodFetch is the Git fetch RPC method.
	MethodFetch = "git.fetch"
	// MethodPush is the Git push RPC method.
	MethodPush = "git.push"
	// MethodRebase is the Git rebase RPC method.
	MethodRebase = "git.rebase"
	// MethodTag is the Git tag RPC method.
	MethodTag = "git.tag"
)

// NewCommitRequest constructs a new commit request.
func NewCommitRequest(id int64, repository, message string, amend bool) ([]byte, error) {
	params, err := json.Marshal(CommitParams{Repository: repository, Message: message, Amend: amend})
	if err != nil {
		return nil, err
	}
	return hostaction.NewRequest(id, MethodCommit, params)
}

// NewFetchRequest constructs a new fetch request.
func NewFetchRequest(id int64, repository string) ([]byte, error) {
	params, err := json.Marshal(RepositoryParams{Repository: repository})
	if err != nil {
		return nil, err
	}
	return hostaction.NewRequest(id, MethodFetch, params)
}

// NewPushRequest constructs a new push request.
func NewPushRequest(id int64, repository, branch, origin string, tags bool) ([]byte, error) {
	params, err := json.Marshal(PushParams{Repository: repository, Branch: branch, Origin: origin, Tags: tags})
	if err != nil {
		return nil, err
	}
	return hostaction.NewRequest(id, MethodPush, params)
}

// NewRebaseRequest constructs a new rebase request.
func NewRebaseRequest(id int64, repository, base string, continueRebase, abort bool) ([]byte, error) {
	params, err := json.Marshal(RebaseParams{Repository: repository, Base: base, Continue: continueRebase, Abort: abort})
	if err != nil {
		return nil, err
	}
	return hostaction.NewRequest(id, MethodRebase, params)
}

// NewTagRequest constructs a new tag request.
func NewTagRequest(id int64, repository, tag, message, target string) ([]byte, error) {
	params, err := json.Marshal(TagParams{Repository: repository, Tag: tag, Message: message, Target: target})
	if err != nil {
		return nil, err
	}
	return hostaction.NewRequest(id, MethodTag, params)
}

// DecodeRepositoryParams decodes and validates repository-only parameters.
func DecodeRepositoryParams(raw json.RawMessage) (RepositoryParams, error) {
	params, err := hostaction.DecodeParams[RepositoryParams](raw)
	if err != nil {
		return RepositoryParams{}, err
	}
	if params.Repository == "" {
		return RepositoryParams{}, errors.New("repository is required")
	}
	return params, nil
}

// DecodeCommitParams decodes and validates commit parameters.
func DecodeCommitParams(raw json.RawMessage) (CommitParams, error) {
	params, err := hostaction.DecodeParams[CommitParams](raw)
	if err != nil {
		return CommitParams{}, err
	}
	if params.Repository == "" {
		return CommitParams{}, errors.New("repository is required")
	}
	if params.Message == "" {
		return CommitParams{}, errors.New("message is required")
	}
	return params, nil
}

// DecodePushParams decodes and validates push parameters.
func DecodePushParams(raw json.RawMessage) (PushParams, error) {
	params, err := hostaction.DecodeParams[PushParams](raw)
	if err != nil {
		return PushParams{}, err
	}
	if params.Repository == "" {
		return PushParams{}, errors.New("repository is required")
	}
	if params.Branch == "" {
		return PushParams{}, errors.New("branch is required")
	}
	return params, nil
}

// DecodeRebaseParams decodes and validates rebase parameters.
func DecodeRebaseParams(raw json.RawMessage) (RebaseParams, error) {
	params, err := hostaction.DecodeParams[RebaseParams](raw)
	if err != nil {
		return RebaseParams{}, err
	}
	if params.Repository == "" {
		return RebaseParams{}, errors.New("repository is required")
	}
	modes := 0
	if params.Base != "" {
		modes++
	}
	if params.Continue {
		modes++
	}
	if params.Abort {
		modes++
	}
	if modes != 1 {
		return RebaseParams{}, errors.New("exactly one of base, continue, or abort is required")
	}
	return params, nil
}

// DecodeTagParams decodes and validates tag parameters.
func DecodeTagParams(raw json.RawMessage) (TagParams, error) {
	params, err := hostaction.DecodeParams[TagParams](raw)
	if err != nil {
		return TagParams{}, err
	}
	if params.Repository == "" {
		return TagParams{}, errors.New("repository is required")
	}
	if params.Tag == "" {
		return TagParams{}, errors.New("tag is required")
	}
	if params.Message == "" {
		return TagParams{}, errors.New("message is required")
	}
	return params, nil
}

// DecodeResult decodes an untyped RPC result as a Git result.
func DecodeResult(result any) (Result, error) {
	return hostaction.DecodeResult[Result](result)
}
