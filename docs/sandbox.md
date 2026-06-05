# Sandbox and Integration Details

This page covers Toby's environment layout, project access rules, runtime context files, and MCP setup.

## Environment

- `XDG_PROJECTS_DIR` defaults to `~/Projects`.
- `XDG_CONFIG_HOME` defaults to `~/.config`; Toby host configuration is loaded from `$XDG_CONFIG_HOME/toby`.
- Toby runs sandboxes in containers and requires a Docker-compatible daemon. It talks to the daemon through the Docker SDK and honors `DOCKER_HOST` and the active Docker context, so Docker Engine, Docker Desktop, Podman, and remote daemons all work.
- The container uses `/toby`: `/toby/home` is `$HOME`, `/toby/workspace` contains mounted projects, `/toby/context` contains generated context, and `/toby/bin` contains the sandbox-facing `toby` binary.

Persistent CLI project directories must resolve to `$XDG_PROJECTS_DIR` or a path below `$XDG_PROJECTS_DIR`. Launch configuration can add named projects from other host paths.

## Projects

For persistent environments, Toby requires an environment name. By default, that environment name is also the project directory name under `$XDG_PROJECTS_DIR`. `toby opencode my-app` uses the home backing Docker volume `toby.default.runtime.home.my-app` and the project `$XDG_PROJECTS_DIR/my-app`.

Use `--project` to point an environment at a different project directory. The value can be an absolute path or a `~`-relative path, but the resolved directory must be `$XDG_PROJECTS_DIR` or below it.

Launch configuration files passed with `toby --config <file>` can define one or more named projects. A configured project's `path` is the host source directory and may be absolute or relative to the config file; if omitted, it defaults to the host `$XDG_PROJECTS_DIR/<name>`. Each configured project is mounted inside the sandbox under the project root at `/toby/workspace/<name>`. In config-owned launches, the first existing configured project is the working directory. In overlay launches such as `toby --config <file> opencode my-app`, the CLI project remains primary and configured projects are additional.

Example:

```yaml
name: review
container:
  image: mcr.microsoft.com/devcontainers/javascript-node:24-bookworm
  build:
    context: .
    dockerfile: Dockerfile.toby
settings:
  mountProfile: review
workdir: ~/tmp
project:
  app:
    primary: true
  foo:
    path: ~/Projects/bar
  qux:
  baz:
    path: /foo/bar
tool:
  opencode:
    primary: true
    params: ["--model", "anthropic/claude-sonnet-4-5"]
    mountProfile: review
  uv:
```

Inside the sandbox, those projects appear under `/toby/workspace/app`, `/toby/workspace/foo`, `/toby/workspace/qux`, and `/toby/workspace/baz`. Toby Git and MCP repository names use those configured project names, not the host source paths.

If a configured project has a null value or no `path`, the host source defaults to the host `$XDG_PROJECTS_DIR/<name>`. Explicit relative project `path` values resolve from the launch config file directory, absolute paths are used as-is, and leading `~` expands to the user's home directory. Toby does not otherwise clean, canonicalize, or resolve symlinks as part of config path expansion.

If `workdir` is set, Toby passes it to the selected runtime after leading `~` expansion to the sandbox home without otherwise resolving or validating it. If omitted, the working directory is the primary configured project's sandbox path.

Configured `tool` entries are object keys. A null value enables the tool with defaults; an object can set `primary`, `params`, and `mountProfile`. `params` is only allowed on the resolved primary tool, and `mountProfile` namespaces that tool's persistent volumes. Tool names must be registered Toby tools. In config-owned launches, the primary tool launches and later tools are installed and made available. Toby parses all CLI arguments before the first `--`; arguments after that first `--`, including later `--` values, are appended to the primary tool's configured `params`. In overlay launches, the CLI-selected tool launches in the foreground and configured tools are additional; duplicate tools are loaded once.

Persistent runtime and tool state lives in container-native Docker named volumes. `settings.mountProfile` namespaces those volumes for a launch (default `default`); `tool.<tool>.mountProfile` selects a different namespace for one tool. A tool requests a mount by `type`/`name`/`purpose` at a container path (`~/…` expands to the container `$HOME`); each request resolves to a lazy Docker volume named `toby.<mountProfile>.<type>.<name>.<purpose>`, managed by Docker. Runtime home uses `toby.<mountProfile>.runtime.home.<sandboxName>`. Toby never bind-mounts the user's host tool configuration. The Docker tool is the exception: it binds `/var/run/docker.sock` and the `$HOME`-based `~/.docker`. Set `settings.suppressWarnings: ["*"]` to suppress all warnings, or set it to a list of warning IDs such as `provider.model-discovery`, `project.autoload-disabled`, `project.duplicate`, or `project.missing`. Synthetic Toby config is generated in all modes.

Configured project paths that do not exist are skipped with `project.missing`. Duplicate configured project names are skipped with `project.duplicate`; the same host source path may be mounted multiple times under different project names. If all configured projects are missing or duplicate in a config-owned launch, Toby exits after printing the warnings. If a CLI project is specified and exists, missing or duplicate configured projects only reduce the additional project set.

Use `exec` as the primary tool to run arbitrary sandbox commands:

```yaml
project:
  foo:
tool:
  exec:
    primary: true
    params: ["npm", "test"]
  npm:
```

`toby --config toby.yaml -- -- --watch` runs `npm test -- --watch` in `/toby/workspace/foo`.

## Sandbox Runtime

Toby mounts only the paths the sandbox requires: provider-backed volumes and selected projects under `/toby/workspace`. Host secrets such as `~/.ssh` and `~/.gnupg` are not mounted into the sandbox. Operations that need host credentials should go through Toby's control bridge instead of copying keys into the environment.

For each sandbox session, the container starts a small shell bootstrap that downloads the sandbox-facing Toby binary from the host control server to `/toby/bin`, marks it executable, and launches it as `toby sandbox manager` by absolute path. The sandbox manager connects to the authenticated WebSocket control endpoint, sends `context.init`, handles generic file commands such as `file.create` and `file.mkdir`, and then runs host-requested `command.run` requests inside the sandbox. File commands default to root-owned regular files (`0644`) and directories (`0755`). Host-requested command execution defaults to the host uid, gid, and supplementary groups.

The host control server listens on `127.0.0.1` with a random per-run bearer token. How the container reaches it depends on the daemon: a local Linux daemon uses host networking and the unchanged `127.0.0.1`; Docker Desktop uses `host.docker.internal`; remote and Podman daemons use testcontainers' host-access tunnel at `host.testcontainers.internal`. The same listener serves `/control` for WebSocket JSON-RPC, `/binary` for the sandbox helper download, and `/proxy/<uuid>` for per-run HTTP proxy targets. The bootstrap and sandbox manager receive calculated `HOME`, `TOBY_CONTROL_HOST=host:port`, and `TOBY_CONTROL_TOKEN`; launched sandbox commands do not receive the control variables but keep `HOME`. Toby does not construct startup environment variables from host values, except the container receives host `TERM` when it is set.

The host manager runs registered context init services after `context.init`. Services add context through the context service, which translates those requests into sandbox manager file commands. When the foreground command exits, the sandbox manager sends `command.exit` with the command UUID and exit code. The host manager then sends `sandbox.terminate`; the sandbox manager exits with code 0, while the host exits with the foreground command's exit code.

### Container Runtime

The container runtime is implemented with the testcontainers-go library (the Docker SDK under the hood) rather than the `docker` CLI. Containers run as `--user 0:0` with an init process, so Dockerfile `USER` lines do not prevent the sandbox manager from owning setup work. The default image is `mcr.microsoft.com/devcontainers/javascript-node:24-bookworm`. A named volume is mounted at `$HOME` (`/toby/home`), and projects live under `/toby/workspace`. The in-container layout (`/toby/home`, `/toby/workspace`, `/toby/bin`, `/toby/context`) is fixed and defined as constants in `container/layout`. The long-lived run container hosts the in-container `toby sandbox manager`, and the interactive agent's terminal is attached to that container.

`$HOME` is backed by a named Docker volume such as `toby.default.runtime.home.my-app`, based on the selected mount profile and sandbox name, so private home state persists across runs without bind mounting the host home contents. Other Toby runtime paths such as `/toby/bin` and `/toby/context` are not persisted by that volume.

Launches use three lifecycle steps. Prime mounts home, projects, host binds, and provider volumes in their final locations so intermediate directories are created. Setup mounts only home and provider volumes at isolated `/toby/mounts/<random>` paths, starts the Toby sandbox manager, and lets runtime/tool setup commands run as root. Run remounts everything in final locations and starts the normal sandbox manager used for context injection and foreground tool execution.

The `workdir` value is a sandbox-visible path. A leading `~` expands to the sandbox home (`/toby/home`).

The container image is responsible for containing the tools required by the selected Toby tools, including `curl` for the bootstrap download. Toby mounts the private home and selected projects; it does not install base OS packages into the image.

Set `container.build.context` to build an image before launch. Relative build contexts resolve from the config file directory, relative `dockerfile` values resolve from the build context, and `dockerfile` defaults to `Dockerfile`. If `image` is set, Toby first checks whether the image exists locally and only builds when it is missing. If `image` is omitted, Toby builds for every launch and uses the resulting image ID, relying on Docker's build cache for repeat runs.

On Linux, the sandbox-facing Toby binary is served from `/proc/self/exe`. macOS release builds embed the matching Linux helper. Local Darwin builds without the release embed tag require `TOBY_LINUX_TOBY` to point at a Linux Toby binary.

Local Linux daemons bind-mount host project paths directly. With a remote daemon, project paths must be reachable by that daemon, and the container reaches the host control server through testcontainers' host-access tunnel (`host.testcontainers.internal`).

## Host Configuration

Toby loads host configuration from `$XDG_CONFIG_HOME/toby/config.json`, `config.yaml`, and `config.yml`, with `~/.config` used when `XDG_CONFIG_HOME` is unset. If multiple files exist, they are deep merged in that order.

Toby config is its own format. Supported top-level keys are `instruction`, `mcp`, `permission`, `provider`, `settings`, `tool`, and `container`; unsupported top-level keys fail config loading. Some nested shapes intentionally mirror OpenCode for convenience:

- `mcp.server` config is rendered into supported synthetic tool config files under the generated context directory. Local entries use `type: local` with `command`; remote entries use `type: remote` with `url`; both are rendered as remote MCP URLs through `http://<control-host>/proxy/<uuid>`. Toby keeps remote upstream URLs and authentication headers on the host. Toby's own MCP server is always injected as `toby` after host config is merged. Toby does not write generated config into regular tool config files such as `~/.codex`, `~/.copilot`, or `~/.grok/config.toml`; Grok uses a `~/.grok/managed_config.toml` symlink back to the generated Grok config.
- `instruction` is an array of host instruction file paths or glob patterns. Relative paths resolve from `$XDG_CONFIG_HOME/toby`. During context init, Toby writes matching files under the generated context directory using the source basename. If two included files share a basename, later files receive a short random suffix before the extension, for example `foobar.1a2b3c.md`.
- `permission.paths` entries are path patterns and permission modes rendered into supported tool configs. Leading `~` expands to the host home directory. Toby also injects default permissions for the sandbox projects root, `/tmp`, and the common sandbox `$HOME` cache/state directories used by Go, npm, and pip (`~/go` and `~/.cache`). Configured entries override generated defaults for the same path.
- `provider` config uses Toby's provider schema. Supported types are `openai` and `anthropic`. Toby keeps upstream `baseURL` and credential `headers` on the host and exposes each provider to tools through `http://<control-host>/proxy/<uuid>`. OpenCode receives these entries translated to `@ai-sdk/openai-compatible` or `@ai-sdk/anthropic`; configured `models` are kept verbatim, otherwise Toby queries `/models` during sandbox startup. If discovery fails, Toby emits `provider.model-discovery` and excludes only that provider from generated OpenCode config.
- `container` config sets global defaults for sandbox launches. CLI flags override launch config values, launch config values override host config defaults, and host config defaults override built-in defaults.

Global mount-profile and warning defaults use the same shape as launch config:

```yaml
settings:
  mountProfile: default
  suppressWarnings: ["*"]
  autoloadProjectConfig: true
tool:
  claude:
    mountProfile: work
```

`settings.mountProfile` (and per-tool `tool.<tool>.mountProfile`) namespaces a tool's persistent Docker volumes. Set `settings.suppressWarnings: ["*"]` to suppress all warning IDs from that config, or provide a list of specific IDs to suppress only those warnings. Set `settings.autoloadProjectConfig: true` to automatically load `<project>/.toby.yaml` for direct launches; when disabled, the presence of `.toby.yaml` emits the suppressible `project.autoload-disabled` warning.

Example global Docker container defaults:

```yaml
container:
  image: mcr.microsoft.com/devcontainers/javascript-node:24-bookworm
```

## MCP

Toby exposes its built-in MCP server through a per-run HTTP proxy URL. The server provides tools for running selected host Git commands for repositories visible through the initial project bind mount, tools for managing Toby-owned local MCP sidecars, and `toby://` resources for guidance and session introspection.

Git MCP calls run through the host Toby process, so host Git config, SSH agents, GPG signing setup, and credential helpers remain available without being mounted into the sandbox.

Toby also exposes each configured MCP server through a per-run HTTP proxy URL. Supported remote entries use `type: remote` and `url`; generated tool config keeps the original MCP name and points the tool at `http://<control-host>/proxy/<uuid>`. The built-in Toby MCP uses the same shape, with the proxy dispatching to the in-process MCP handler. The host Toby process opens upstream endpoints for configured remote MCPs and applies configured headers or host environment tokens.

Local MCP entries use `type: local` and are started as session-scoped sidecars, then rendered to tools as remote/http MCP URLs. `transport: stdio` runs the configured command and bridges stdio to streamable HTTP in the host Toby process. `transport: http` runs the configured command and proxies to the configured port/path. Sidecars do not run `toby sandbox manager`, do not receive setup hooks, and do not mount projects, generated context, or managed state. MCP sidecars run with the selected image defaults for user, home, and working directory. MCP sidecars run as Docker containers; by default they use the main sandbox image, and each server can override it with a per-server `image`.

Available tools:

- `git.commit`: run `git commit -m MESSAGE` on the host for a visible repository, or `git commit --amend -m MESSAGE` when `amend` is true. It commits only staged files and does not add files.
- `git.fetch`: run `git fetch` on the host for a visible repository.
- `git.push`: run `git push ORIGIN BRANCH` on the host for a visible repository, optionally with `--tags`. `origin` defaults to `origin`.
- `git.rebase`: run `git rebase BASE`, `git rebase --continue`, or `git rebase --abort` on the host for a visible repository.
- `git.tag`: run `git tag -a TAG -m MESSAGE [TARGET]` on the host for a visible repository. `target` defaults to `HEAD`.
- `mcp.start`: start a configured local MCP sidecar by name.
- `mcp.stop`: stop a configured local MCP sidecar by name.
- `mcp.restart`: restart a configured local MCP sidecar by name.

Available resources:

- `toby://docs/git`: guidance for host Git MCP tools.
- `toby://docs/mcps`: guidance for Toby-managed MCP proxying and lifecycle tools.
- `toby://docs/introspection`: guidance for session introspection resources and redaction behavior.
- `toby://session/runtime`: current Toby version, debug mode, sandbox runtime, sandbox-visible paths, and runtime-defined details.
- `toby://session/mcps`: configured MCP status and redacted runtime details.
- `toby://session/tools`: active and available Toby tools plus provider summaries.
- `toby://session/projects`: visible projects, additional binds, and managed mounts.

Session resources never expose provider or MCP headers, URLs, commands, argv, or environment values, even when `settings.debug: true` or `--debug` is active.

The Git MCP tools accept repository names relative to the sandbox project root (`/toby/workspace`), including nested repositories such as `foo/bar/baz`. The requested repository must already be visible in the sandbox through the initial project bind mount. Invalid or non-visible repository names are rejected on the host before Git runs. Use these tools instead of running `git commit`, `git fetch`, `git push`, `git rebase`, or `git tag` directly in the sandbox when host Git config, GPG keys, or SSH keys are required.

## OpenCode

For OpenCode sandboxes, Toby sets `OPENCODE_CONFIG_DIR` to the generated OpenCode context directory. That generated directory contains a `.gitignore` and `opencode.json` with host Toby config translated for OpenCode, the Toby MCP server, configured instructions, the injected default and configured permission paths as `external_directory` rules, and provider entries pointed at Toby's `/proxy/<uuid>` proxy. Model discovery failures emit `provider.model-discovery` to stderr and omit only the provider that failed discovery.

Equivalent generated OpenCode `opencode.json` entry:

```json
{
  "$schema": "https://opencode.ai/config.json",
  "mcp": {
    "toby": {
      "type": "remote",
      "url": "http://<control-host>/proxy/<uuid>",
      "enabled": true
    }
  }
}
```

## Claude Code

For Claude Code sandboxes, Toby sets `CLAUDE_CONFIG_DIR` to `$HOME/.config/claude` so Claude writes credentials, history, and session state into its normal config directory, which is persisted by its managed Docker volume. Toby injects generated context through launch flags instead of pointing Claude's config directory at the generated Claude context files, and launches `claude` with:

- `--mcp-config .../claude/mcp.json` adds the Toby MCP server.
- `--append-system-prompt-file .../claude/instructions.md` appends configured Toby instruction files.
- `--settings .../claude/settings.json` passes generated Claude settings, including the injected default and configured permission directories as `permissions.additionalDirectories` (glob entries are omitted, since Claude takes directory paths).

Generated `claude/mcp.json`:

```json
{
  "mcpServers": {
    "toby": {
      "type": "http",
      "url": "http://<control-host>/proxy/<uuid>"
    }
  }
}
```

## Codex

For Codex sandboxes, Toby injects its built-in MCP server, configured MCP entries, and combined instructions through launch-time config overrides. Configured MCP entries use per-run `/proxy/<uuid>` URLs, including local sidecars. It does not write to `CODEX_HOME`, does not create a profile file, and does not pass configured HTTP MCP credentials as argv values. The launch includes overrides equivalent to:

```sh
codex \
  -c 'mcp_servers.toby.url="http://<control-host>/proxy/<uuid>"' \
  -c 'mcp_servers.toby.enabled=true' \
  -c 'developer_instructions="..."'
```

Codex has no session config-file flag for arbitrary MCP config, so Toby uses launch-time `-c` overrides instead of writing regular Codex config files.

## Copilot

For Copilot sandboxes, Toby generates `copilot/mcp-config.json` and `copilot/AGENTS.md` under the generated context directory, sets `COPILOT_CUSTOM_INSTRUCTIONS_DIRS` to include the generated Copilot context directory, and launches Copilot with:

```sh
copilot --additional-mcp-config @<generated-context>/copilot/mcp-config.json
```

Generated `copilot/mcp-config.json`:

```json
{
  "mcpServers": {
    "toby": {
      "type": "http",
      "url": "http://<control-host>/proxy/<uuid>",
      "tools": ["*"]
    }
  }
}
```

## Grok

For Grok sandboxes, Toby keeps Grok state in the normal `.grok` managed mount so `tools.grok.mountProfile` works like other tools. Toby generates `grok/config.toml` under the generated context directory, then links `~/.grok/managed_config.toml` to that generated file during sandbox startup so Grok discovers Toby MCP through its native config loader. Combined instructions are passed with `--rules`.

The generated Grok config contains Toby's MCP server and does not write to `~/.grok/config.toml`.
