package git

// RepositoryParams and the related types define the JSON-RPC parameter and
// result shapes for Git methods.

// RepositoryParams identifies a sandbox-visible repository.
type RepositoryParams struct {
	Repository string `json:"repository" jsonschema:"repository name visible in the sandbox, relative to the sandbox project root"`
}

// CommitParams describes a commit request.
type CommitParams struct {
	Repository string `json:"repository" jsonschema:"repository name visible in the sandbox, relative to the sandbox project root"`
	Message    string `json:"message" jsonschema:"commit message passed to git commit -m"`
	Amend      bool   `json:"amend,omitempty" jsonschema:"amend the previous commit when true"`
}

// PushParams describes a push request.
type PushParams struct {
	Repository string `json:"repository" jsonschema:"repository name visible in the sandbox, relative to the sandbox project root"`
	Branch     string `json:"branch" jsonschema:"single branch to push"`
	Origin     string `json:"origin,omitempty" jsonschema:"remote name to push to, defaults to origin"`
	Tags       bool   `json:"tags,omitempty" jsonschema:"push all tags with --tags when true"`
}

// RebaseParams describes a rebase request.
type RebaseParams struct {
	Repository string `json:"repository" jsonschema:"repository name visible in the sandbox, relative to the sandbox project root"`
	Base       string `json:"base,omitempty" jsonschema:"base ref to rebase onto"`
	Continue   bool   `json:"continue,omitempty" jsonschema:"continue an in-progress rebase when true"`
	Abort      bool   `json:"abort,omitempty" jsonschema:"abort an in-progress rebase when true"`
}

// TagParams describes an annotated tag request.
type TagParams struct {
	Repository string `json:"repository" jsonschema:"repository name visible in the sandbox, relative to the sandbox project root"`
	Tag        string `json:"tag" jsonschema:"annotated tag name to create"`
	Message    string `json:"message" jsonschema:"tag message passed to git tag -m"`
	Target     string `json:"target,omitempty" jsonschema:"optional object to tag, defaults to HEAD"`
}

// Result contains captured Git command output and its exit code.
type Result struct {
	Repository string `json:"repository" jsonschema:"repository name used for the git command"`
	ExitCode   int    `json:"exit_code" jsonschema:"git process exit code"`
	Stdout     string `json:"stdout" jsonschema:"git standard output"`
	Stderr     string `json:"stderr" jsonschema:"git standard error"`
}
