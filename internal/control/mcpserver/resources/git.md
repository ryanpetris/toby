# Toby Git

Use Toby Git tools when working inside a Toby sandbox and the operation should use host Git configuration, SSH agents, GPG signing setup, or credential helpers.

Available tools:

- `git.commit`: commit staged files in a visible repository using host Git. It commits only already staged files and does not add files. Set `amend` to update the previous commit.
- `git.fetch`: fetch remote refs for a visible repository.
- `git.push`: push one branch to a remote, optionally with tags. `origin` defaults to `origin`.
- `git.rebase`: start a rebase onto a base ref, continue an in-progress rebase, or abort an in-progress rebase.
- `git.tag`: create an annotated tag, optionally targeting a specific object.

Repository names are sandbox-visible project or repository names relative to `XDG_PROJECTS_DIR`; nested repositories such as `foo/bar` are supported when they are visible in the sandbox. Invalid or non-visible repository names are rejected by Toby on the host before Git runs.

Prefer these tools over running sandbox-local Git when the task depends on host credentials, signing, or Git configuration. Inspect `git status` and diffs before committing. Stage only intended files; `git.commit` never stages files for you.
