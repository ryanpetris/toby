# Sandbox and Integration Details

This page covers Toby's environment layout, project access rules, runtime context files, and MCP setup.

## Environment

- `XDG_PROJECTS_DIR` defaults to `~/Projects`.
- `XDG_CONFIG_HOME` defaults to `~/.config`; Toby host configuration is loaded from `$XDG_CONFIG_HOME/toby`. If `XDG_CONFIG_HOME` is unset, Toby also accepts `XDG_CONFIG_DIR` before falling back to `~/.config`.
- `XDG_CACHE_HOME` defaults to `~/.cache`; Bubblewrap sandbox homes are stored under `$XDG_CACHE_HOME/toby/sandboxes` unless configured with `sandbox.runtime.bubblewrap.root`.
- Toby selects the available sandbox runtime with the lowest priority number. Docker has priority 0 and Bubblewrap has priority 1, making Docker the default when available.
- Toby uses `/tmp/toby` inside the sandbox for its runtime files, generated context, and sandbox-facing `toby` binary.

Persistent CLI project directories must resolve to `$XDG_PROJECTS_DIR` or a path below `$XDG_PROJECTS_DIR`. Launch configuration can add named projects from other host paths.

## Projects

For persistent environments, Toby requires an environment name. By default, that environment name is also the project directory name under `$XDG_PROJECTS_DIR`. `toby opencode my-app` uses the Bubblewrap sandbox home `$XDG_CACHE_HOME/toby/sandboxes/my-app` and the project `$XDG_PROJECTS_DIR/my-app`.

Use `--project` to point an environment at a different project directory. The value can be an absolute path or a `~`-relative path, but the resolved directory must be `$XDG_PROJECTS_DIR` or below it.

Launch configuration files passed with `toby --config <file>` can define one or more named projects. A configured project's `path` is the host source directory and may be absolute or relative to the config file; if omitted, it defaults to the config file directory. Each configured project is mounted inside the sandbox at `$XDG_PROJECTS_DIR/<name>` regardless of where the source lives on the host. In config-owned launches, the first existing configured project is the working directory. In overlay launches such as `toby --config <file> opencode my-app`, the CLI project remains primary and configured projects are additional.

Example:

```yaml
sandbox:
  name: review
  runtime:
    default: docker
    docker:
      image: node:lts-bookworm
      build:
        context: .
        dockerfile: Dockerfile.toby
    bubblewrap:
      root: .toby/sandboxes
  tools:
    default:
      state: private
    opencode:
      state: host
      stateRoot: .toby/opencode-state
  suppressWarnings:
    - tool.host-state
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

Configured `tools` entries can be strings or objects with `name`; `params` is only allowed on the first tool. Tool names must be registered Toby tools. In config-owned launches, the first tool launches, later tools are installed and made available in order, and CLI arguments after `--` are appended to the first tool's configured `params`. In overlay launches, the CLI-selected tool launches in the foreground and configured tools are additional; duplicate tools are loaded once.

`sandbox.tools` controls where each selected tool stores its own state. The default state is `private`, which lets each environment use its private sandbox home and avoids bind mounting host tool directories such as `~/.config/claude` or `~/.local/share/opencode`. Set `state: host` to bind mount state for a tool from `stateRoot`, which is treated like `$HOME` for the tool's known state paths. If `stateRoot` is omitted, host state uses the host `$HOME`. Relative `stateRoot` paths in launch config resolve from the launch config file directory. The Docker tool defaults to host state unless `docker.state` is explicitly set to `private`; its `/var/run/docker.sock` bind remains enabled even when Docker state is private. Toby emits the `tool.host-state` warning when host state is enabled for non-Docker tools because concurrent instances can corrupt shared tool databases. Set `sandbox.suppressWarnings: true` to suppress all warnings, or set it to a list of warning IDs such as `tool.host-state`, `opencode.model-discovery`, `project.autoload-disabled`, or `project.missing`. Synthetic Toby config is generated in both modes.

Configured project paths that do not exist are skipped with `project.missing`. If all configured projects are missing in a config-owned launch, Toby exits after printing the warnings. If a CLI project is specified and exists, missing configured projects only reduce the additional project set.

For example, OpenCode with `stateRoot: .toby/opencode-state` in a config file at `/repo/toby.yaml` uses `/repo/.toby/opencode-state/.config/opencode` and `/repo/.toby/opencode-state/.local/share/opencode` as the host sources.

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

For each sandbox session, the selected runtime starts a small shell bootstrap that downloads the sandbox-facing Toby binary from the host control server to `/tmp/toby/bin/toby`, marks it executable, and launches `/tmp/toby/bin/toby sandbox manager`. The sandbox manager connects to the authenticated WebSocket control endpoint, sends `context.init`, handles generic file commands such as `file.create` and `file.mkdir`, and then runs host-requested `command.run` requests inside the sandbox.

The host control server listens on `127.0.0.1` with a random per-run bearer token. Docker on macOS connects through `host.docker.internal`. The same listener serves `/toby/control` for WebSocket JSON-RPC and `/toby/binary` for the sandbox helper download. `/tmp/toby/bin` is prepended to `PATH`.

The host manager runs registered context init services after `context.init`. Services add context through the context service, which translates those requests into sandbox manager file commands. When the foreground command exits, the sandbox manager sends `command.exit` with the command UUID and exit code. The host manager then sends `sandbox.terminate`; the sandbox manager exits with code 0, while the host exits with the foreground command's exit code.

### Docker Runtime

Docker-backed sandboxes use `docker run --rm --init` with the selected image. The default image is `node:lts-bookworm`. Docker uses the same `$HOME` path and projects path as the host by default. These paths can be overridden in launch config with `sandbox.runtime.docker.home` and `sandbox.runtime.docker.projects`.

Docker `$HOME` is backed by a named Docker volume such as `toby-home-my-app`, based on the sandbox name, so it persists across runs without bind mounting the host home contents.

Docker `sandbox.runtime.docker.home`, `sandbox.runtime.docker.projects`, and `workdir` values are sandbox-visible paths. A leading `~` expands to the Docker sandbox home.

The Docker image is responsible for containing the tools required by the selected Toby tools, including `curl` for the bootstrap download. Toby mounts the private home and selected projects; it does not install base OS packages into the image.

Set `sandbox.runtime.docker.build.context` to build an image before launch. Relative build contexts resolve from the config file directory, relative `dockerfile` values resolve from the build context, and `dockerfile` defaults to `Dockerfile`. If `image` is set, Toby first checks `docker image inspect <image>` and only builds when the image is missing locally. If `image` is omitted, Toby runs `docker build --iidfile ...` for every launch and uses the resulting image ID, relying on Docker's build cache for repeat runs.

On Linux, the sandbox-facing Toby binary is served from `/proc/self/exe`. macOS release builds embed the matching Linux helper. Local Darwin builds without the release embed tag require `TOBY_LINUX_TOBY` to point at a Linux Toby binary.

Docker bind mounts require a local Docker daemon that can access the same host paths as Toby. Remote Docker contexts are not expected to work reliably with host project mounts.

### Bubblewrap Runtime

Bubblewrap-backed sandboxes store private homes under `${XDG_CACHE_HOME:-~/.cache}/toby/sandboxes` by default. Set `sandbox.runtime.bubblewrap.root` in host or launch config to use another host directory. Absolute paths are used as-is, `~` expands to the host home, and relative paths resolve from the config file directory.

If no runtime-specific settings are needed, `sandbox.runtime` can be a string such as `runtime: bubblewrap`. Use the object form with `default` when setting runtime-specific options.

## Host Configuration

Toby loads host configuration from `$XDG_CONFIG_HOME/toby/config.json`, `config.jsonc`, `config.yaml`, and `config.yml`, with `XDG_CONFIG_DIR` and then `~/.config` used when `XDG_CONFIG_HOME` is unset. If multiple files exist, they are deep merged in that order.

Toby config is its own format. Supported top-level keys are `instructions`, `mcp`, `permission`, `provider`, and `sandbox`; unsupported top-level keys fail config loading. Some nested shapes intentionally mirror OpenCode for convenience:

- `mcp` config is rendered into OpenCode and Claude Code synthetic MCP files. Toby's own MCP server is always injected as `toby` after host config is merged.
- `instructions` is an array of host instruction file paths or glob patterns. Relative paths resolve from `$XDG_CONFIG_HOME/toby`. During context init, Toby writes matching files under `/tmp/toby/context/instructions/` using the source basename. If two included files share a basename, later files receive a short random suffix before the extension, for example `foobar.1a2b3c.md`.
- `provider` config uses OpenCode's provider schema and currently applies to OpenCode only. If a provider has a `models` field, Toby keeps it verbatim. If an OpenAI-compatible provider omits `models`, Toby queries `/models` during sandbox startup. If discovery fails, Toby emits the `opencode.model-discovery` warning on stderr and excludes that provider from the generated OpenCode config.
- `sandbox` config sets global defaults for sandbox launches. CLI flags override launch config values, launch config values override host config defaults, and host config defaults override built-in defaults.

Global tool state defaults use the same shape as launch config:

```yaml
sandbox:
  tools:
    default:
      state: private
    claude:
      state: host
      stateRoot: ~/tool-state/claude
  suppressWarnings:
    - tool.host-state
  autoloadProjectConfig: true
```

Relative `stateRoot` paths in global Toby config resolve from the Toby config file directory. Relative `--tool-state-root` values on direct CLI launches resolve from the selected project root. Set `sandbox.suppressWarnings: true` to suppress all warning IDs from that config, or provide a list of IDs to suppress only those warnings. Set `sandbox.autoloadProjectConfig: true` to automatically load `<project>/.toby.yaml` for direct launches; when disabled, the presence of `.toby.yaml` emits the suppressible `project.autoload-disabled` warning.

Example global Docker sandbox defaults:

```yaml
sandbox:
  runtime:
    default: docker
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

For OpenCode sandboxes, Toby sets `OPENCODE_CONFIG_DIR=/tmp/toby/context/opencode`. That generated directory contains a `.gitignore` and `opencode.json` with host Toby config, the Toby MCP server, `/tmp/toby/context/GIT_AGENTS.md` and configured instructions, allowed external-directory rules for `/tmp` and `XDG_PROJECTS_DIR`, and discovered model lists for OpenAI-compatible providers that need them. Model discovery failures emit `opencode.model-discovery` to stderr and omit only the provider that failed discovery.

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

For Claude Code sandboxes, Toby injects its generated context through launch flags rather than by redirecting the config directory. Claude Code writes credentials, history, and session state into its normal config directory, which uses private sandbox state unless host tool state is enabled. Toby generates files under `/tmp/toby/context/claude/` and launches `claude` with:

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
