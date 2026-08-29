# Toby Git

Use Toby Git tools when working inside a Toby sandbox and the operation should use host Git configuration, SSH agents, GPG signing setup, or credential helpers.

Available tools (clients may prefix or regroup these names):

- `git_commit`: commit staged files in a visible repository using host Git. It commits only already staged files and does not add files. Set `amend` to update the previous commit.
- `git_fetch`: fetch remote refs for a visible repository.
- `git_push`: push one branch to a remote, optionally with tags. `origin` defaults to `origin`.
- `git_rebase`: start a rebase onto a base ref, continue an in-progress rebase, or abort an in-progress rebase.
- `git_tag`: create an annotated tag, optionally targeting a specific object.

Repository names are sandbox-visible project or repository names relative to `XDG_PROJECTS_DIR`; nested repositories such as `foo/bar` are supported when they are visible in the sandbox. Invalid or non-visible repository names are rejected by Toby on the host before Git runs.

Prefer these tools over running sandbox-local Git when the task depends on host credentials, signing, or Git configuration. Inspect `git status` and diffs before committing. Stage only intended files; `git_commit` never stages files for you.

Any of these tools may return an approval-required result instead of running (by default `git_push` does unless the user enabled yolo or configured the action). That result names an approval id and the command the user must run in another terminal: `toby approvals <id>`. Relay that command to the user, then call `approvals_wait` with `{"approval":"<id>"}`; once the user approves, the held operation runs on the host and `approvals_wait` returns its git result (exit code, stdout, stderr) exactly as the original tool would have. Do not re-run the git tool while its approval is pending - that would only report the same approval id. If the user denies, stop and ask for new instructions.
