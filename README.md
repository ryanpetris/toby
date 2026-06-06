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
- Toby MCP bridges host Git operations that need your host SSH agent, GPG setup, Git config, or credential helpers.

## Install

Toby is a Go command-line tool:

```sh
go install petris.dev/toby@latest
```

Make sure your Go binary directory, usually `~/go/bin`, is on `PATH`.

Runtime requirements:

- A Docker-compatible daemon is required. Toby talks to it through the Docker SDK (via testcontainers-go), so Docker Engine, Docker Desktop, Podman, and remote daemons all work; Toby honors `DOCKER_HOST` and the active Docker context.
- macOS release builds embed the Linux sandbox helper used inside the container. Local Darwin builds without the release embed tag require `TOBY_LINUX_TOBY` to point at a Linux Toby binary.
- Tool-specific installers may need common utilities such as `curl`, `tar`, or `npm` inside the sandbox image.

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

## Projects Directory

Toby follows the XDG-style `XDG_PROJECTS_DIR` convention. If `XDG_PROJECTS_DIR` is unset, Toby uses `~/Projects`.

The default project for a named environment is `$XDG_PROJECTS_DIR/<env>`. For example, `toby codex website` uses a persistent sandbox home named `website` and mounts the project at `~/Projects/website` by default.

Use `--project` when the sandbox name and project directory should differ:

```sh
toby claude review-env --project ~/Projects/customer-api
```

Project paths must resolve to `XDG_PROJECTS_DIR` or a directory below it. This keeps sandbox project access explicit and prevents accidental access to unrelated host paths.

## Host Configuration

Toby reads host configuration from `${XDG_CONFIG_HOME:-~/.config}/toby/config.json`, `config.yaml`, and `config.yml`. If more than one file exists, Toby deep merges them in that order.

Toby config is its own format. Supported top-level keys are `instruction`, `mcp`, `permission`, `provider`, `settings`, `tool`, and `container`; unsupported top-level keys fail config loading. Some nested shapes intentionally mirror OpenCode for convenience:

- `mcp.server` entries are Toby-managed and exposed to supported tools through per-run `/proxy/<uuid>` URLs, alongside Toby's built-in MCP server. Remote entries use `type: remote` with `url`; Toby opens the upstream connection and resolves configured `headers` on the host side. Local entries use `type: local` with `command`; Toby runs them as session-scoped MCP sidecars and proxies them as remote MCP URLs. Configure non-proxied tool-native MCPs in the tool's own config instead.
- `instruction` entries are host instruction file paths. Relative paths resolve from the Toby config directory. Toby copies them into the generated context directory inside the sandbox using the source filename, adding a short random suffix before the extension if two files share a filename.
- `permission.paths` entries are path patterns and permission modes used for generated tool configs. Leading `~` expands to the host home directory. Toby injects default permissions for the sandbox projects root, `/tmp`, and the common sandbox `$HOME` cache directories for Go, npm, and pip (`~/go` and `~/.cache`); configured entries override generated defaults for the same path.
- `provider` entries are Toby provider declarations. Supported provider types are `openai` for OpenAI-compatible APIs and `anthropic` for Anthropic-compatible APIs. Toby exposes each provider to supported tools through a per-run `/proxy/<uuid>` URL, so upstream `baseURL` and credential `headers` stay on the host. OpenCode receives these providers translated to `@ai-sdk/openai-compatible` or `@ai-sdk/anthropic`; configured `models` are used verbatim, otherwise Toby queries `/models` on the upstream provider during sandbox startup. Discovery failures log `provider.model-discovery` and leave only that provider out of generated OpenCode config.
- `container` sets global defaults for sandbox launches. CLI flags override launch config values, launch config values override host config defaults, and host config defaults override built-in defaults.

```yaml
provider:
  local:
    type: openai
    baseURL: https://api.example.com/v1
    headers:
      Authorization: "Bearer {env:EXAMPLE_API_KEY}"
    models:
      example-model: {}
permission:
  paths:
    ~/shared: allow
    ~/shared/**: allow
```

Persistent tool and runtime state lives in container-native Docker named volumes. A mount *profile* namespaces those volumes so you can keep separate sets of state: the default profile is `default`, `settings.mountProfile` selects another for a launch, and `tool.<tool>.mountProfile` selects a different one for a single tool. The Docker tool is an exception — it bind-mounts `/var/run/docker.sock` and the `$HOME`-based `~/.docker` instead of using a managed volume.

```yaml
settings:
  mountProfile: default # optional; namespaces persistent volumes (default: default)
  autoloadProjectConfig: true # optional; load <project>/.toby.yaml on direct launches
  debug: false # optional; when true, preserve Docker containers and expose host/container debug info through Toby MCP
tool:
  opencode:
    mountProfile: work # optional; namespaces this tool's volumes separately
```

Tool volumes are named `toby.<mountProfile>.tool.<tool>.<purpose>` and the runtime home volume `toby.<mountProfile>.runtime.home.<sandboxName>`; Docker manages them and they persist across runs. A tool addresses its mounts with container paths (`~/…` expands to the container `$HOME`); Toby never bind-mounts the user's host tool configuration. Set `settings.suppressWarnings: ["*"]` to suppress all warnings, or set it to a list of warning IDs such as `provider.model-discovery`, `project.autoload-disabled`, `project.duplicate`, or `project.missing`. Toby still generates synthetic tool config in all modes.

Set `settings.autoloadProjectConfig: true` in host config to load `<project>/.toby.yaml` during direct launches such as `toby opencode my-app`. If `.toby.yaml` exists and autoload is disabled, Toby emits `project.autoload-disabled`. In autoload mode, the CLI tool and project stay foreground and primary; tools and projects from `.toby.yaml` are added, with duplicate project names skipped after warning.

Example global Docker container defaults:

```yaml
container:
  image: mcr.microsoft.com/devcontainers/javascript-node:24-bookworm
  build:
    context: ~/docker/toby
mcp:
  image: ghcr.io/acme/toby-mcp-base:latest # optional default for MCP sidecars
```

A reachable Docker socket is required. Podman and remote daemons work through the standard `DOCKER_HOST` environment variable (for example `DOCKER_HOST=unix:///run/user/1000/podman/podman.sock`); there is no runtime selection in Toby.

## Launch Configuration

Use `--config` to launch from a per-run YAML or JSON file. JSON files are parsed with the same YAML parser. If no tool and project are specified on the CLI, the first configured tool is foreground and the first existing configured project is primary. If a CLI tool and project are specified, for example `toby --config myconfig.yaml opencode my-app`, the CLI tool and project stay foreground and primary while the config contributes sandbox settings, extra tools, and extra projects.

```yaml
name: foo # optional; defaults to the first project name
container:
  image: mcr.microsoft.com/devcontainers/javascript-node:24-bookworm # optional; defaults to mcr.microsoft.com/devcontainers/javascript-node:24-bookworm
  build: # optional; build an image before launch
    context: . # defaults to this config file's directory
    dockerfile: Dockerfile.toby # optional; relative to context, defaults to Dockerfile
settings:
  autoUpgrade: true # optional; defaults to false
  debug: false # optional; overrides global settings.debug for this launch
  mountProfile: work # optional; namespaces this launch's persistent volumes
  suppressWarnings: ["*"] # optional; list of warning IDs, or ["*"] to suppress all
workdir: ~/tmp # optional; defaults to the primary project path inside the sandbox
project:
  foo:
    primary: true
  baz: # source defaults to $XDG_PROJECTS_DIR/baz
  bar:
    path: ../bar-source # optional source; relative to this config file, leading ~ expands
tool:
  opencode:
    primary: true
    params: ["--model", "anthropic/claude-sonnet-4-5"] # optional; only valid on the primary tool
    mountProfile: work # optional; namespaces this tool's persistent volumes
  uv:
  npm:
```

In config-owned launches, the primary project is the working directory. Set `primary: true` when multiple projects are configured; if there is exactly one project, Toby infers it. Configured project paths that do not exist are skipped with the suppressible `project.missing` warning. Duplicate configured project names are skipped with the suppressible `project.duplicate` warning; the same host source path may be mounted multiple times under different project names. In overlay launches, the CLI project remains first and configured projects are additional. In config-owned launches, the primary tool is the foreground tool. Set `primary: true` when multiple tools are configured; if there is exactly one tool, Toby infers it. In overlay launches, the CLI tool is primary and configured tools are additional. `params` is only applied when that tool is the resolved primary tool. Tool names must be registered Toby tools, such as `opencode`, `exec`, `uv`, or `npm`.

Persistent mounts use named Docker volumes, managed by Docker.

Path values in launch config expand a leading `~` to the user's home directory. Toby does not otherwise clean, canonicalize, or resolve symlinks as part of config path expansion.

`workdir` is passed to the selected sandbox runtime after leading `~` expansion to the sandbox home and is not otherwise resolved or validated by Toby. If omitted, Toby uses the first configured project's sandbox path.

Toby parses all arguments before the first `--`; command arguments must come after it. Everything after that first `--`, including later `--` values, is appended to the primary tool's configured `params`:

```sh
toby --config myconfig.yaml -- --additional-param value
```

Use `exec` as the primary tool to run arbitrary sandbox commands from `params` or from CLI arguments.

Configured project `path` values are host source directories. If a project is a string or an object with only `name`, the host source defaults to the host `$XDG_PROJECTS_DIR/<name>`. Explicit relative `path` values resolve from the launch config file directory, absolute paths are used as-is, and leading `~` expands to the host home. Each project appears inside the sandbox under the project root at `/toby/workspace/<name>`.

```yaml
project:
  foo:
tool:
  exec:
    primary: true
    params: ["npm", "test"]
  npm:
```

```sh
toby --config myconfig.yaml -- -- --watch
```

This runs `npm test -- --watch` in `/toby/workspace/foo`.

The sandbox uses `/toby`: `$HOME` is `/toby/home`, projects mount under `/toby/workspace`, generated context lives under `/toby/context`, and the helper binary is copied to `/toby/bin/toby` with `docker cp`. Only `$HOME` is persisted, in a named Docker volume. With `settings.debug: true` or `--debug`, Toby leaves the sandbox and MCP sidecar containers running after exit for inspection instead of terminating them; containers are never reused. Toby does not construct startup environment variables from host values; it explicitly sets only calculated `HOME` plus `TOBY_SANDBOX=1` for the sandbox manager, and passes host `TERM` to the container when it is set. Per-command environment is injected into each `docker exec`. The sandbox image is responsible for containing the tools needed by the selected Toby tools; use `container.image` when a custom image is required. Set `container.build.context` to build an image from a Dockerfile. Relative build contexts resolve from the config file directory; relative `dockerfile` values resolve from the build context and default to `Dockerfile`. If `image` is set, Toby uses it when it already exists locally and builds it otherwise. If `image` is omitted, Toby always builds and uses the resulting image ID.

The `workdir` value is a sandbox-visible path. A leading `~` expands to the sandbox home (`/toby/home`).

## Common Commands

```sh
toby opencode <env>
toby claude <env>
toby codex <env>
toby exec <env> -- <command arguments>
```

Useful flags:

- `--project <dir>` selects a project directory under `XDG_PROJECTS_DIR`.
- `--image <image>` selects the container image for direct launches.
- `--config <file>` launches from a YAML or JSON launch configuration.
- `--debug` enables debug mode for the launch, overriding config.
- `--install` installs the selected tool and exits.
- `--upgrade` reinstalls the selected tool, then launches it.

## MCP

Toby automatically exposes a sandbox-only MCP server to supported tools launched through `toby <client>`. The built-in server is registered as a per-run `/proxy/<uuid>` target, like configured remote MCP servers, and provides `git.commit`, `git.fetch`, `git.push`, `git.rebase`, `git.tag`, `mcp.start`, `mcp.stop`, and `mcp.restart`, plus `toby://docs/...` and `toby://session/...` resources. Git tools operate on repositories already visible in the sandbox. Session resources never expose provider/MCP headers, URLs, commands, argv, or environment values; host paths, Docker volume names, container names, and local MCP host ports are included only when debug mode is enabled. For OpenCode, Claude Code, Copilot, and Grok, Toby injects this server through synthetic tool configuration generated under the context directory. Grok discovers that generated config through a `~/.grok/managed_config.toml` symlink. Codex receives Toby MCP through launch-time `-c` config overrides instead of a generated profile file.

The sandbox manager connects back to the host over a gRPC link carried on the container's stdio; there is no control host or token to pass in. `HOME` remains available to commands. MCP and provider proxy URLs point at the manager's in-container loopback listener (`http://127.77.0.1:47600/proxy/<uuid>`), which tunnels each connection to the host reverse proxy.

Configured MCP servers are exposed through per-run HTTP proxy URLs with their original configured names. For example, an `mcp.server.docs` entry using `type: remote` and `url: https://example.com/mcp` is rendered to supported tools as a remote MCP pointing at `http://<control-host>/proxy/<uuid>`. Toby opens remote upstream connections from the host process and applies configured `headers` there, resolving any `{env:VAR}` and `{file:path}` substitutions on the host so credentials never enter the sandbox.

Local MCP entries use `type: local`, `command`, and optional `transport: stdio` (default) or `transport: http`. Toby starts them asynchronously as managerless sidecars with no project/context/managed mounts. MCP sidecars run as Docker containers; by default they use the main sandbox image, and each MCP can override it with a per-server `image`. MCP sidecars use that `image` and the image defaults for user, home, and working directory. If you do not want Toby proxying, configure the MCP directly in the tool's own config instead.

Toby does not write generated config into regular tool config files such as `~/.codex`, `~/.copilot`, or `~/.grok/config.toml`; Grok's `managed_config.toml` symlink points back to the generated Grok config. Tool-specific instruction injection is also session-scoped: Copilot receives a generated `AGENTS.md` directory through `COPILOT_CUSTOM_INSTRUCTIONS_DIRS`, Grok receives combined rules through `--rules`, and Codex receives combined developer instructions through `-c developer_instructions=...`.

Inside the sandbox, Toby downloads the sandbox-facing Toby binary as `toby` and runs the hidden `toby sandbox manager` command to manage the session.

## Tools

Toby launches one **primary** (foreground) tool and can install others
alongside it. Available tools:

| Tool (`toby <name>`) | CLI | Group | What it is |
| --- | --- | --- | --- |
| `opencode` | `opencode` | AI | OpenCode coding agent |
| `claude` | `claude` | AI | Claude Code |
| `codex` | `codex` | AI | OpenAI Codex |
| `copilot` | `copilot` | AI | GitHub Copilot CLI |
| `grok` | `grok` | AI | Grok CLI |
| `speckit` | `specify` | AI | GitHub Spec Kit |
| `t3` | `t3` | UI | T3 Code launcher (drives the coding tools) |
| `emdash` | `emdash` | UI | Emdash |
| `docker` | `docker` | System | Host Docker access |
| `npm` | `npm` | System | Node package manager |
| `uv` | `uv` | System | Python package/tool manager |
| `github_cli` | `gh` | VCS | GitHub CLI |
| `gitlab_cli` | `glab` | VCS | GitLab CLI |
| `fj` | `fj` | VCS | Forgejo CLI |
| `exec` | (command) | Command | Run an arbitrary sandbox command |

For OpenCode, Claude Code, Codex, Copilot, and Grok, Toby generates synthetic
configuration (MCP servers, providers, and instructions) without touching the
tools' normal config files. See [docs/tools.md](docs/tools.md) for the install
mechanism and config injection per tool.

### Running T3 Code with coding tools

`t3` is a launcher that can drive the other coding tools. Use `--with-<tool>` to
install and wire up each tool you want available inside it:

```sh
toby t3 my-app --with-claude
toby t3 my-app --with-claude --with-codex --with-opencode
```

Each enabled tool is installed into the sandbox and gets its Toby integration
(the `git.*` MCP server, configured providers, and your instruction files)
generated, so it works as soon as you select it inside t3. The declarative
equivalent lists t3 first and the coding tools after it:

```yaml
project:
  my-app:
tool:
  t3:
    primary: true
  claude:
  codex:
  opencode:
```

```sh
toby --config t3.yaml
```

## More Docs

- [Architecture](docs/architecture.md) — host/sandbox split, control protocol, runtimes, launch flow.
- [Configuration reference](docs/configuration.md) — host config, launch config, managed mounts, warnings.
- [Tools](docs/tools.md) — per-tool install and synthetic config, including the t3 walkthrough.
- [Examples](docs/examples.md) — end-to-end recipes.
- [Sandbox and integration details](docs/sandbox.md) — generated artifacts and per-tool integration surface.
- [Debugging sandbox startup](docs/debugging-sandbox-startup.md) — runbook for bring-up failures, host prerequisites, and dogfooding path setup.
