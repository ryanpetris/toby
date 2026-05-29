# Sandbox and Integration Details

This page covers Toby's environment layout, project access rules, runtime context files, and MCP setup.

## Environment

- `XDG_PROJECTS_DIR` defaults to `~/Projects`.
- `XDG_CONFIG_HOME` defaults to `~/.config`; Toby host configuration is loaded from `$XDG_CONFIG_HOME/toby`. If `XDG_CONFIG_HOME` is unset, Toby also accepts `XDG_CONFIG_DIR` before falling back to `~/.config`.
- `XDG_CACHE_HOME` defaults to `~/.cache`; sandbox homes are stored under `$XDG_CACHE_HOME/toby/sandboxes`.
- `XDG_RUNTIME_DIR` is required. Toby uses `$XDG_RUNTIME_DIR/toby` inside the sandbox for its private socket, generated context files, and the sandbox-facing `toby` binary mount.
- `TOBY_SANDBOX_ROOT` overrides the sandbox home root when set.

Project directories must resolve to `$XDG_PROJECTS_DIR` or a path below `$XDG_PROJECTS_DIR`. Toby bind mounts only the selected project directory into the sandbox.

## Projects

For persistent environments, Toby requires an environment name. By default, that environment name is also the project directory name under `$XDG_PROJECTS_DIR`. `toby opencode my-app` uses the sandbox home `$XDG_CACHE_HOME/toby/sandboxes/my-app` and the project `$XDG_PROJECTS_DIR/my-app`.

Use `--project` to point an environment at a different project directory. The value can be an absolute path or a `~`-relative path, but the resolved directory must be `$XDG_PROJECTS_DIR` or below it.

For temporary environments, use `--tmp-env` with either a project name or `--project`. A bare project name such as `my-app` resolves to `$XDG_PROJECTS_DIR/my-app`; a path value is resolved and checked against `$XDG_PROJECTS_DIR`.

## Sandbox Runtime

Toby bind mounts the private sandbox `$HOME` directly and bind mounts the selected project directory. Host secrets such as `~/.ssh` and `~/.gnupg` are not mounted into the sandbox. Operations that need host credentials should go through Toby's control bridge instead of copying keys into the environment.

For each sandbox session, Bubblewrap launches `$XDG_RUNTIME_DIR/toby/bin/toby sandbox manager`. The sandbox manager connects to `$XDG_RUNTIME_DIR/toby/sandbox.sock`, sends `context.init`, handles generic file commands such as `file.create` and `file.mkdir`, and then runs host-requested `command.run` requests inside the sandbox.

The host control server listens on `$XDG_RUNTIME_DIR/toby/control/<pid>.sock` and bind mounts that socket into the sandbox as `$XDG_RUNTIME_DIR/toby/sandbox.sock`. The current Toby executable is bind-mounted into the sandbox as `$XDG_RUNTIME_DIR/toby/bin/toby`, and that directory is prepended to `PATH`.

The host manager runs registered context init services after `context.init`. Services add context through the context service, which translates those requests into sandbox manager file commands. When the foreground command exits, the sandbox manager sends `command.exit` with the command UUID and exit code. The host manager then sends `sandbox.terminate`; the sandbox manager exits with code 0, while the host exits with the foreground command's exit code.

## Host Configuration

Toby loads host configuration from `$XDG_CONFIG_HOME/toby/config.json`, `config.jsonc`, `config.yaml`, and `config.yml`, with `XDG_CONFIG_DIR` and then `~/.config` used when `XDG_CONFIG_HOME` is unset. If multiple files exist, they are deep merged in that order.

Toby config is its own format. Supported top-level keys are `instructions`, `mcp`, `permission`, and `provider`; unsupported top-level keys fail config loading. Some nested shapes intentionally mirror OpenCode for convenience:

- `mcp` config is rendered into OpenCode and Claude Code synthetic MCP files. Toby's own MCP server is always injected as `toby` after host config is merged.
- `instructions` is an array of host instruction file paths or glob patterns. Relative paths resolve from `$XDG_CONFIG_HOME/toby`. During context init, Toby writes matching files under `$XDG_RUNTIME_DIR/toby/context/instructions/` using the source basename. If two included files share a basename, later files receive a short random suffix before the extension, for example `foobar.1a2b3c.md`.
- `provider` config uses OpenCode's provider schema and currently applies to OpenCode only. If a provider has a `models` field, Toby keeps it verbatim. If an OpenAI-compatible provider omits `models`, Toby queries `/models` during sandbox startup. If discovery fails, Toby warns on stderr and excludes that provider from the generated OpenCode config.

## MCP

Toby exposes an MCP stdio server inside each sandbox as `toby sandbox mcp`. The server provides tools for running selected host Git commands for repositories visible through the initial project bind mount.

Git MCP calls run through the host Toby process, so host Git config, SSH agents, GPG signing setup, and credential helpers remain available without being mounted into the sandbox.

Available tools:

- `git.commit`: run `git commit -m MESSAGE` on the host for a visible repository. It commits only staged files and does not add files.
- `git.fetch`: run `git fetch` on the host for a visible repository.
- `git.push`: run `git push ORIGIN BRANCH` on the host for a visible repository. `origin` defaults to `origin`.

The Git MCP tools accept repository names relative to `XDG_PROJECTS_DIR`, including nested repositories such as `foo/bar/baz`. The requested repository must already be visible in the sandbox through the initial project bind mount. Repository names with empty, `.`, or `..` path segments are rejected. Use these tools instead of running `git commit`, `git fetch`, or `git push` directly in the sandbox when host Git config, GPG keys, or SSH keys are required.

The same control calls are available inside a sandbox as CLI commands: `toby sandbox git commit REPOSITORY -m MESSAGE`, `toby sandbox git fetch REPOSITORY`, and `toby sandbox git push REPOSITORY BRANCH [ORIGIN]`.

## OpenCode

For OpenCode sandboxes, Toby sets `OPENCODE_CONFIG_DIR=$XDG_RUNTIME_DIR/toby/context/opencode`. That generated directory contains a `.gitignore` and `opencode.json` with host Toby config, the Toby MCP server, `$XDG_RUNTIME_DIR/toby/context/GIT_AGENTS.md` and configured instructions, allowed external-directory rules for `/tmp` and `XDG_PROJECTS_DIR`, and discovered model lists for OpenAI-compatible providers that need them. Model discovery failures warn to stderr and omit only the provider that failed discovery.

Equivalent generated OpenCode `opencode.json` entry:

```json
{
  "$schema": "https://opencode.ai/config.json",
  "mcp": {
    "toby": {
      "type": "local",
      "command": ["toby", "sandbox", "mcp"],
      "enabled": true
    }
  }
}
```

## Claude Code

For Claude Code sandboxes, Toby injects its generated context through launch flags rather than by redirecting the config directory: Claude Code writes credentials, history, and session state into `CLAUDE_CONFIG_DIR`, so that directory stays the writable real config bind-mounted under `TOBY_SANDBOX_ROOT/.config/claude/`. Toby generates files under `$XDG_RUNTIME_DIR/toby/context/claude/` and launches `claude` with:

- `--mcp-config .../claude/mcp.json` adds the Toby MCP server.
- `--append-system-prompt-file .../claude/instructions.md` appends `GIT_AGENTS.md` and configured Toby instruction files.
- `--settings .../claude/settings.json` allows the `/tmp` and project-root directories via `permissions.additionalDirectories`, mirroring OpenCode's external-directory rules.

Generated `claude/mcp.json`:

```json
{
  "mcpServers": {
    "toby": {
      "type": "stdio",
      "command": "toby",
      "args": ["sandbox", "mcp"]
    }
  }
}
```
