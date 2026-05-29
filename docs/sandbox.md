# Sandbox and Integration Details

This page covers Toby's environment layout, project access rules, FUSE-backed runtime features, and MCP setup.

## Environment

- `XDG_PROJECTS_DIR` defaults to `~/Projects`.
- `XDG_CACHE_HOME` defaults to `~/.cache`; sandbox homes are stored under `$XDG_CACHE_HOME/toby/sandboxes`.
- `XDG_STATE_HOME` defaults to `$HOME/.local/state`; Toby runtime files are exposed under `$XDG_STATE_HOME/toby` inside the sandbox, with shared generated/static files under `$XDG_STATE_HOME/toby/static`.
- `TOBY_SANDBOX_ROOT` overrides the sandbox home root when set.

Project directories must resolve to `$XDG_PROJECTS_DIR` or a path below `$XDG_PROJECTS_DIR`. The FUSE runtime root is read-only and only exposes selected or approved projects under `$XDG_PROJECTS_DIR`.

## Projects

For persistent environments, Toby requires an environment name. By default, that environment name is also the project directory name under `$XDG_PROJECTS_DIR`. `toby opencode my-app` uses the sandbox home `$XDG_CACHE_HOME/toby/sandboxes/my-app` and the project `$XDG_PROJECTS_DIR/my-app`.

Use `--project` to point an environment at a different project directory. The value can be an absolute path or a `~`-relative path, but the resolved directory must be `$XDG_PROJECTS_DIR` or below it.

For temporary environments, use `--tmp-env` with either a project name or `--project`. A bare project name such as `my-app` resolves to `$XDG_PROJECTS_DIR/my-app`; a path value is resolved and checked against `$XDG_PROJECTS_DIR`.

## Sandbox Runtime

Toby bind mounts the private sandbox `$HOME` directly. A separate in-process FUSE runtime tree exposes approved project mounts, Toby control files, and generated OpenCode integration config.

The sandbox home is not your host home. Toby does not mount host secrets such as `~/.ssh` or `~/.gnupg` into the sandbox. Operations that need host credentials should go through Toby's control bridge instead of copying keys into the environment.

If FUSE is unavailable or fails to start, Toby prints a warning to stderr and continues without Toby MCP, sandbox control commands, synthetic configuration, or mountable projects. If `--mountable-projects` was explicitly requested, missing or failing FUSE is an error.

Toby prepends `$XDG_STATE_HOME/toby/bin` to sandbox `PATH` and exposes the current Toby binary there, so MCP clients launched inside the sandbox can use `toby mcp` directly. The command is intended to run inside a Toby sandbox and fails when `$XDG_STATE_HOME/toby/control` is unavailable. The Toby runtime directory is synthetic and does not pass through files from the sandbox home; attempts to create or modify entries there fail.

## MCP

When FUSE is available, Toby exposes an MCP stdio server inside each sandbox as `toby mcp`. The server provides tools for running selected host Git commands for visible repositories. Passing `--mountable-projects` also enables tools for listing projects, reading project README files, and requesting project mounts.

Git MCP calls run through the host Toby process, so host Git config, SSH agents, GPG signing setup, and credential helpers remain available without being mounted into the sandbox.

Available tools:

- `git_commit`: run `git commit -m MESSAGE` on the host for a visible repository. It commits only staged files and does not add files.
- `git_fetch`: run `git fetch` on the host for a visible repository.
- `git_push`: run `git push ORIGIN BRANCH` on the host for a visible repository. `origin` defaults to `origin`.
- `project_list` with `--mountable-projects`: list project directories under `XDG_PROJECTS_DIR`.
- `project_readme` with `--mountable-projects`: read `README.md` from a project by directory name without mounting it.
- `project_mount` with `--mountable-projects`: ask the host user to approve mounting a project directory from `XDG_PROJECTS_DIR` into the current sandbox.

`project_list` and `project_readme` do not prompt for confirmation. Use `project_mount` when a task needs access to another project directory. It accepts project directory names, not arbitrary paths. Confirmation opens a tmux popup; without tmux the tool returns an error. Without `--mountable-projects`, Toby bind mounts only the selected project directory instead of exposing the FUSE-backed project root.

The Git MCP tools accept repository names relative to `XDG_PROJECTS_DIR`, including nested repositories such as `foo/bar/baz`. The requested repository must already be visible in the sandbox through the initial project bind mount or `project_mount`; the tools do not mount projects. Repository names with empty, `.`, or `..` path segments are rejected. Use these tools instead of running `git commit`, `git fetch`, or `git push` directly in the sandbox when host Git config, GPG keys, or SSH keys are required.

The same control calls are available inside a sandbox as CLI commands: `toby sandbox git commit REPOSITORY -m MESSAGE`, `toby sandbox git fetch REPOSITORY`, `toby sandbox git push REPOSITORY BRANCH [ORIGIN]`, `toby sandbox project list`, `toby sandbox project readme NAME`, and `toby sandbox project mount NAME`.

## OpenCode

For OpenCode sandboxes with FUSE available, Toby sets `OPENCODE_CONFIG_DIR=$XDG_STATE_HOME/toby/static/opencode`. That read-only directory contains a generated `.gitignore` and `opencode.json` with the Toby MCP server, `$XDG_STATE_HOME/toby/static/GIT_AGENTS.md` instructions, allowed external-directory rules for `/tmp` and `XDG_PROJECTS_DIR`, and best-effort model lists for OpenAI-compatible providers.

With `--mountable-projects`, Toby also adds `$XDG_STATE_HOME/toby/static/PROJECT_MOUNT_AGENTS.md` and exposes `commands/toby.project.mount.md`, providing the `/toby.project.mount` command to ask OpenCode to mount a project through the Toby MCP server. Model lists are fetched during sandbox startup; fetch failures warn to stderr and continue. The real config remains under `TOBY_SANDBOX_ROOT/.config/opencode/` and does not need these entries.

Equivalent manual OpenCode `opencode.json` entry:

```json
{
  "$schema": "https://opencode.ai/config.json",
  "mcp": {
    "toby": {
      "type": "local",
      "command": ["toby", "mcp"],
      "enabled": true
    }
  }
}
```

## Claude Code

For Claude Code sandboxes with FUSE available, Toby injects its synthetic configuration through launch flags rather than by redirecting the config directory: Claude Code writes credentials, history, and session state into `CLAUDE_CONFIG_DIR`, so that directory stays the writable real config bind-mounted under `TOBY_SANDBOX_ROOT/.config/claude/`. Toby generates read-only files under `$XDG_STATE_HOME/toby/static/claude/` and launches `claude` with:

- `--mcp-config .../claude/mcp.json` adds the Toby MCP server (no trust prompt, since the config is passed explicitly).
- `--append-system-prompt-file .../claude/instructions.md` appends `GIT_AGENTS.md` (and `PROJECT_MOUNT_AGENTS.md` with `--mountable-projects`).
- `--settings .../claude/settings.json` allows the `/tmp` and project-root directories via `permissions.additionalDirectories`, mirroring OpenCode's external-directory rules.
- `--plugin-dir .../claude/plugin` (with `--mountable-projects`) provides the `/toby:toby-project-mount` command to request a project mount through the Toby MCP server.

If FUSE is unavailable, Toby launches `claude` with no extra flags and only the real config is used.

Generated `claude/mcp.json`:

```json
{
  "mcpServers": {
    "toby": {
      "type": "stdio",
      "command": "toby",
      "args": ["mcp"]
    }
  }
}
```

## Manual Client Setup

Toby configures the clients below automatically when launched through `toby <client>`. The entries here are for using the `toby mcp` server from a client that Toby did not launch.

Claude Code:

```sh
claude mcp add toby -- toby mcp
```

Codex CLI `~/.codex/config.toml`:

```toml
[mcp_servers.toby]
command = "toby"
args = ["mcp"]
```

Equivalent Codex CLI command:

```sh
codex mcp add toby -- toby mcp
```

GitHub Copilot CLI `~/.copilot/mcp-config.json`:

```json
{
  "mcpServers": {
    "toby": {
      "command": "toby",
      "args": ["mcp"]
    }
  }
}
```

If Copilot CLI prompts for a transport type, choose `STDIO`.
