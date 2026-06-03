# Toby Sandbox

You are running inside a Toby sandbox: a private-home development sandbox on a host machine. The host's Git configuration, SSH agents/keys, and GPG/PGP signing keys are **not** available inside the sandbox. Running `git` directly for operations that need them will fail or produce unsigned/misattributed results:

- Commits that should be signed or use the host's user identity.
- `fetch`, `push`, and other network operations that authenticate over SSH or host credential helpers.

For those operations, call the Toby MCP tools instead. They run on the **host**, where Git configuration, SSH agents, GPG signing, and credential helpers are available.

## Git tools

- `git.commit`: commit already staged files in a visible repository using host Git. It does not stage files for you; set `amend` to update the previous commit.
- `git.fetch`: fetch remote refs for a visible repository.
- `git.push`: push one branch to a remote (`origin` defaults to `origin`), optionally with all tags.
- `git.rebase`: start a rebase onto a base ref, or continue/abort an in-progress rebase.
- `git.tag`: create an annotated tag, optionally targeting a specific object.

Repository names are sandbox-visible project or repository names relative to `XDG_PROJECTS_DIR`; nested repositories such as `foo/bar` are supported when visible. Invalid or non-visible names are rejected on the host before Git runs.

Stage and inspect changes with sandbox-local Git (`git add`, `git status`, `git diff`) — those are safe and need no host credentials — then use `git.commit` to record them. Prefer the Toby Git tools over sandbox-local Git whenever the task depends on host credentials, signing, or Git configuration.

## MCP tools

Toby-configured MCP servers are exposed to sandboxed tools through per-run proxy URLs under their configured names. Local Toby-managed sidecars have lifecycle tools:

- `mcp.start`: start a configured local Toby-managed MCP sidecar.
- `mcp.stop`: stop a configured local Toby-managed MCP sidecar.
- `mcp.restart`: restart a configured local Toby-managed MCP sidecar.

These apply only to local Toby-managed sidecars; remote MCPs are proxied endpoints and are not started or stopped by them.

## Introspection resources

Read these read-only `toby://` resources for guidance and current session state:

- `toby://docs/git`, `toby://docs/mcps`, `toby://docs/introspection`: detailed guidance on the Git tools, Toby-managed MCP sidecars, and introspection behavior.
- `toby://session/runtime`: Toby version, debug mode, sandbox runtime, and sandbox-visible runtime paths.
- `toby://session/mcps`: configured MCP server status and redacted runtime details.
- `toby://session/tools`: primary tool, active and available Toby tools, tool groups, and provider summaries.
- `toby://session/projects`: visible projects, additional binds, and managed mount summaries.

Toby introspection never exposes configured provider or MCP URLs, headers, commands, argv, or environment values, even in debug mode.
