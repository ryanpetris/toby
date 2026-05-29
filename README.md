<p align="center">
  <img src="docs/logo.png" alt="Toby" width="280">
</p>

Toby sandboxes software development tools so agents can work on code without inheriting your real home directory.

Run OpenCode, Claude Code, Codex, Copilot, Grok, Docker, package managers, and VCS CLIs in private Linux homes with access only to the project you choose. Your host `~/.ssh`, `~/.gnupg`, and other personal files stay outside the sandbox.

When a sandboxed agent needs to do something that really should happen on the host, like signing a commit or pushing over SSH, Toby exposes a narrow MCP bridge. The agent can ask Toby to run selected host Git operations for visible repositories without mounting your keys into the sandbox.

## Sandbox Development

- Each named environment gets its own private `$HOME`.
- Project access is scoped to your XDG Projects directory.
- Tool installers write into the sandbox home, not your host home.
- Temporary environments make one-off tasks disposable.
- Toby MCP bridges host Git operations that need your host SSH agent, GPG setup, Git config, or credential helpers.

## Install

Toby is a Go command-line tool:

```sh
go install petris.dev/toby@latest
```

Make sure your Go binary directory, usually `~/go/bin`, is on `PATH`.

Runtime requirements:

- Linux with Bubblewrap available at `/usr/bin/bwrap`.
- `XDG_RUNTIME_DIR` must be set.
- Tool-specific installers may need common utilities such as `curl`, `tar`, or `npm`.

## Get Started

Create or choose a project under your Projects directory:

```sh
mkdir -p ~/Projects/my-app
```

Launch OpenCode in a persistent sandbox named `my-app`:

```sh
toby opencode my-app
```

Toby maps the environment name to `~/Projects/my-app`, creates a private sandbox home for that environment, installs missing tool dependencies inside that home, then launches the tool in the project.

Run any command in the same environment:

```sh
toby exec my-app -- npm test
```

Use a temporary home for disposable work:

```sh
toby exec --tmp-env --project ~/Projects/my-app -- bash
```

## Projects Directory

Toby follows the XDG-style `XDG_PROJECTS_DIR` convention. If `XDG_PROJECTS_DIR` is unset, Toby uses `~/Projects`.

The default project for a named environment is `$XDG_PROJECTS_DIR/<env>`. For example, `toby codex website` uses a persistent sandbox home named `website` and mounts the project at `~/Projects/website` by default.

Use `--project` when the sandbox name and project directory should differ:

```sh
toby claude review-env --project ~/Projects/customer-api
```

Project paths must resolve to `XDG_PROJECTS_DIR` or a directory below it. This keeps sandbox project access explicit and prevents accidental access to unrelated host paths.

## Host Configuration

Toby reads host configuration from `${XDG_CONFIG_HOME:-~/.config}/toby/config.json`, `config.jsonc`, `config.yaml`, and `config.yml`; if `XDG_CONFIG_HOME` is unset, Toby also accepts `XDG_CONFIG_DIR` before falling back to `~/.config`. If more than one file exists, Toby deep merges them in that order.

Toby config is its own format. Supported top-level keys are `instructions`, `mcp`, `permission`, and `provider`; unsupported top-level keys fail config loading. Some nested shapes intentionally mirror OpenCode for convenience:

- `mcp` entries are added to supported generated tool configs, alongside Toby's built-in MCP server.
- `instructions` entries are host instruction file paths. Relative paths resolve from the Toby config directory. Toby copies them into `$XDG_RUNTIME_DIR/toby/context/instructions/` using the source filename, adding a short random suffix before the extension if two files share a filename.
- `provider` entries use OpenCode's provider schema and are currently applied to OpenCode only. If a provider includes `models`, Toby uses those models verbatim. For OpenAI-compatible providers without `models`, Toby queries the provider at sandbox startup; if discovery fails, Toby logs a warning and leaves that provider out of the generated OpenCode config.

## Common Commands

```sh
toby opencode <env>
toby claude <env>
toby codex <env>
toby exec <env> -- <command arguments>
```

Useful flags:

- `--project <dir>` selects a project directory under `XDG_PROJECTS_DIR`.
- `--tmp-env` uses a temporary sandbox home that is removed on exit.
- `--install` installs the selected tool and exits.
- `--upgrade` reinstalls the selected tool, then launches it.

## MCP

Toby automatically exposes a sandbox-only `toby-sandbox mcp` server to supported tools launched through `toby <client>`. The server uses a private Unix socket at `$XDG_RUNTIME_DIR/toby/sandbox.sock` inside the sandbox and provides `git_commit`, `git_fetch`, and `git_push` for repositories already visible in the sandbox.

The `toby-sandbox` command is the same Toby binary bind-mounted under a sandbox-only name. It is not part of the public host CLI.

## More Docs

- [Sandbox and integration details](docs/sandbox.md)
