# Toby Git

Use Toby control tools for `git commit`, `git fetch`, and `git push` when working inside a Toby sandbox. These commands run on the host so host Git config, GPG keys, and SSH keys are available.

Available Toby MCP tools:

- `git.commit`: commit staged files in a visible repository using host Git.
- `git.fetch`: fetch remote refs in a visible repository using host Git.
- `git.push`: push one branch from a visible repository using host Git.

Equivalent CLI commands inside the sandbox:

- `toby sandbox git commit REPOSITORY -m MESSAGE`
- `toby sandbox git fetch REPOSITORY`
- `toby sandbox git push REPOSITORY BRANCH [ORIGIN]`

Repository names are relative to `XDG_PROJECTS_DIR`, may include nested paths such as `foo/bar/baz`, must already be visible in the sandbox, and must not contain `.` or `..` path segments. `git.commit` commits only already staged files; it does not add files.
