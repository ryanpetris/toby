# Toby Git

Use Toby control tools for `git commit`, `git fetch`, `git push`, `git rebase`, and `git tag` when working inside a Toby sandbox. These commands run on the host so host Git config, GPG keys, and SSH keys are available.

Available Toby MCP tools:

- `git.commit`: commit staged files in a visible repository using host Git, optionally amending the previous commit.
- `git.fetch`: fetch remote refs in a visible repository using host Git.
- `git.push`: push one branch from a visible repository using host Git, optionally with all tags.
- `git.rebase`: start, continue, or abort a rebase in a visible repository using host Git.
- `git.tag`: create an annotated tag in a visible repository using host Git.

Repository names are relative to `XDG_PROJECTS_DIR`, may include nested paths such as `foo/bar/baz`, must already be visible in the sandbox, and must not contain `.` or `..` path segments. `git.commit` commits only already staged files; it does not add files.
