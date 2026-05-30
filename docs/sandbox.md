# Sandbox and Integration Details

This page covers Toby's environment layout, project access rules, runtime context files, and MCP setup.

## Environment

- `XDG_PROJECTS_DIR` defaults to `~/Projects`.
- `XDG_CONFIG_HOME` defaults to `~/.config`; Toby host configuration is loaded from `$XDG_CONFIG_HOME/toby`. If `XDG_CONFIG_HOME` is unset, Toby also accepts `XDG_CONFIG_DIR` before falling back to `~/.config`.
- `XDG_CACHE_HOME` defaults to `~/.cache`; sandbox homes are stored under `$XDG_CACHE_HOME/toby/sandboxes`.
- `XDG_RUNTIME_DIR` is required. Toby uses `$XDG_RUNTIME_DIR/toby` inside the sandbox for its private socket, generated context files, and the sandbox-facing `toby` binary mount.
- `TOBY_SANDBOX_ROOT` overrides the sandbox home root when set.
- The default sandbox runtime is Bubblewrap. Launch configs can set `sandbox.runtime: docker` to use Docker instead.

Project directories must resolve to `$XDG_PROJECTS_DIR` or a path below `$XDG_PROJECTS_DIR`. Toby bind mounts only the selected project directory into the sandbox.

## Projects

For persistent environments, Toby requires an environment name. By default, that environment name is also the project directory name under `$XDG_PROJECTS_DIR`. `toby opencode my-app` uses the sandbox home `$XDG_CACHE_HOME/toby/sandboxes/my-app` and the project `$XDG_PROJECTS_DIR/my-app`.

Use `--project` to point an environment at a different project directory. The value can be an absolute path or a `~`-relative path, but the resolved directory must be `$XDG_PROJECTS_DIR` or below it.

Launch configuration files passed with `toby --config <file>` can define one or more named projects. A configured project's `path` is the host source directory and may be absolute or relative to the config file; if omitted, it defaults to the config file directory. Each configured project is mounted inside the sandbox at `$XDG_PROJECTS_DIR/<name>` regardless of where the source lives on the host. The first configured project is the working directory.

Example:

```yaml
sandbox:
  name: review
  runtime: docker
  docker:
    image: node:lts-bookworm
workdir: ~/tmp
projects:
  - name: foo
    path: ~/Projects/bar
  - name: baz
    path: /foo/bar
tools:
  - name: opencode
    params: ["--model", "anthropic/claude-sonnet-4-5"]
  - uv
```

Inside both Bubblewrap and Docker sandboxes, those projects appear under the sandbox-visible projects directory, `$XDG_PROJECTS_DIR/foo` and `$XDG_PROJECTS_DIR/baz` by default. Toby Git and MCP repository names use those configured project names, not the host source paths.

Path values in launch config expand a leading `~` to the user's home directory. Toby does not otherwise clean, canonicalize, or resolve symlinks as part of config path expansion.

If `workdir` is set, Toby passes it to the selected runtime after leading `~` expansion to the sandbox home without otherwise resolving or validating it. If omitted, the working directory is the first configured project's sandbox path.

Configured `tools` entries can be strings or objects with `name`; `params` is only allowed on the first tool. Tool names must be registered Toby tools. The first tool launches, and later tools are installed and made available in order. CLI arguments after `--` are appended to the first tool's configured `params`.

Use `exec` as the first tool to run arbitrary sandbox commands:

```yaml
projects:
  - foo
tools:
  - name: exec
    params: ["npm", "test"]
  - npm
```

`toby --config toby.yaml -- -- --watch` runs `npm test -- --watch` in `$XDG_PROJECTS_DIR/foo`.

## Sandbox Runtime

Toby bind mounts the private sandbox `$HOME` directly and bind mounts the selected project directory. Host secrets such as `~/.ssh` and `~/.gnupg` are not mounted into the sandbox. Operations that need host credentials should go through Toby's control bridge instead of copying keys into the environment.

For each sandbox session, the selected runtime launches `$XDG_RUNTIME_DIR/toby/bin/toby sandbox manager`. The sandbox manager connects to `$XDG_RUNTIME_DIR/toby/sandbox.sock`, sends `context.init`, handles generic file commands such as `file.create` and `file.mkdir`, and then runs host-requested `command.run` requests inside the sandbox.

The host control server listens on `$XDG_RUNTIME_DIR/toby/control/<pid>.sock` and bind mounts that socket into the sandbox as `$XDG_RUNTIME_DIR/toby/sandbox.sock`. The current Toby executable is bind-mounted into the sandbox as `$XDG_RUNTIME_DIR/toby/bin/toby`, and that directory is prepended to `PATH`.

The host manager runs registered context init services after `context.init`. Services add context through the context service, which translates those requests into sandbox manager file commands. When the foreground command exits, the sandbox manager sends `command.exit` with the command UUID and exit code. The host manager then sends `sandbox.terminate`; the sandbox manager exits with code 0, while the host exits with the foreground command's exit code.

### Docker Runtime

Docker-backed sandboxes use `docker run --rm --init` with the selected image. The default image is `node:lts-bookworm`. Docker uses the same `$HOME` path and projects path as the host by default. These paths can be overridden in launch config with `sandbox.docker.home` and `sandbox.docker.projects`.

Docker `$HOME` is backed by a named Docker volume such as `toby-home-my-app`, based on the sandbox name, so it persists across runs without bind mounting the host home contents.

Docker `sandbox.docker.home`, `sandbox.docker.projects`, and `workdir` values are sandbox-visible paths. A leading `~` expands to the Docker sandbox home.

The Docker image is responsible for containing the tools required by the selected Toby tools. Toby mounts the private home, selected projects, Toby runtime directory, control socket, and sandbox-facing Toby binary; it does not install base OS packages into the image.

Docker bind mounts require a local Docker daemon that can access the same host paths as Toby. Remote Docker contexts are not expected to work reliably with host project mounts.

## Host Configuration

Toby loads host configuration from `$XDG_CONFIG_HOME/toby/config.json`, `config.jsonc`, `config.yaml`, and `config.yml`, with `XDG_CONFIG_DIR` and then `~/.config` used when `XDG_CONFIG_HOME` is unset. If multiple files exist, they are deep merged in that order.

Toby config is its own format. Supported top-level keys are `instructions`, `mcp`, `permission`, `provider`, and `sandbox`; unsupported top-level keys fail config loading. Some nested shapes intentionally mirror OpenCode for convenience:

- `mcp` config is rendered into OpenCode and Claude Code synthetic MCP files. Toby's own MCP server is always injected as `toby` after host config is merged.
- `instructions` is an array of host instruction file paths or glob patterns. Relative paths resolve from `$XDG_CONFIG_HOME/toby`. During context init, Toby writes matching files under `$XDG_RUNTIME_DIR/toby/context/instructions/` using the source basename. If two included files share a basename, later files receive a short random suffix before the extension, for example `foobar.1a2b3c.md`.
- `provider` config uses OpenCode's provider schema and currently applies to OpenCode only. If a provider has a `models` field, Toby keeps it verbatim. If an OpenAI-compatible provider omits `models`, Toby queries `/models` during sandbox startup. If discovery fails, Toby warns on stderr and excludes that provider from the generated OpenCode config.
- `sandbox` config sets global defaults for sandbox launches. CLI flags override launch config values, launch config values override host config defaults, and host config defaults override built-in defaults.

Example global Docker sandbox defaults:

```yaml
sandbox:
  runtime: docker
  docker:
    image: node:lts-bookworm
```

## MCP

Toby exposes an MCP stdio server inside each sandbox as `toby sandbox mcp`. The server provides tools for running selected host Git commands for repositories visible through the initial project bind mount.

Git MCP calls run through the host Toby process, so host Git config, SSH agents, GPG signing setup, and credential helpers remain available without being mounted into the sandbox.

Available tools:

- `git.commit`: run `git commit -m MESSAGE` on the host for a visible repository, or `git commit --amend -m MESSAGE` when `amend` is true. It commits only staged files and does not add files.
- `git.fetch`: run `git fetch` on the host for a visible repository.
- `git.push`: run `git push ORIGIN BRANCH` on the host for a visible repository, optionally with `--tags`. `origin` defaults to `origin`.
- `git.rebase`: run `git rebase BASE`, `git rebase --continue`, or `git rebase --abort` on the host for a visible repository.
- `git.tag`: run `git tag -a TAG -m MESSAGE [TARGET]` on the host for a visible repository. `target` defaults to `HEAD`.

The Git MCP tools accept repository names relative to `XDG_PROJECTS_DIR`, including nested repositories such as `foo/bar/baz`. The requested repository must already be visible in the sandbox through the initial project bind mount. Repository names with empty, `.`, or `..` path segments are rejected. Use these tools instead of running `git commit`, `git fetch`, `git push`, `git rebase`, or `git tag` directly in the sandbox when host Git config, GPG keys, or SSH keys are required.

The same control calls are available inside a sandbox as CLI commands: `toby sandbox git commit REPOSITORY -m MESSAGE [--amend]`, `toby sandbox git fetch REPOSITORY`, `toby sandbox git push REPOSITORY BRANCH [ORIGIN] [--tags]`, `toby sandbox git rebase REPOSITORY BASE|--continue|--abort`, and `toby sandbox git tag REPOSITORY TAG -m MESSAGE [TARGET]`.

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
