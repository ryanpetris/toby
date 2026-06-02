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

- Docker for the highest-priority sandbox runtime. On Linux, Bubblewrap is used as a lower-priority fallback when `bwrap` is available.
- `curl` must be available inside the sandbox image or Bubblewrap environment so Toby can download its sandbox helper at startup.
- macOS release builds embed the Linux sandbox helper used inside Docker. Local Darwin builds without the release embed tag require `TOBY_LINUX_TOBY` to point at a Linux Toby binary.
- Tool-specific installers may need common utilities such as `tar` or `npm`.

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

Toby reads host configuration from `${XDG_CONFIG_HOME:-~/.config}/toby/config.json`, `config.jsonc`, `config.yaml`, and `config.yml`. If more than one file exists, Toby deep merges them in that order.

Toby config is its own format. Supported top-level keys are `instructions`, `mcps`, `permissions`, `providers`, `mountProfiles`, `settings`, `tools`, and `sandbox`; unsupported top-level keys fail config loading. Some nested shapes intentionally mirror OpenCode for convenience:

- `mcps` entries are added to supported generated tool configs, alongside Toby's built-in MCP server. Local entries use `type: local` with `command`; remote entries use `type: remote` with `url`. Remote entries are exposed through per-run `/proxy/<uuid>` URLs, and Toby resolves configured headers on the host side before registering the proxy target. Generated tool config lives under Toby's generated context directory inside the sandbox and does not modify the tools' normal config files.
- `instructions` entries are host instruction file paths. Relative paths resolve from the Toby config directory. Toby copies them into the generated context directory inside the sandbox using the source filename, adding a short random suffix before the extension if two files share a filename.
- `permissions.paths` entries are host path patterns and permission modes used for generated tool configs. Leading `~` expands to the host home directory.
- `providers` entries are Toby provider declarations. Supported provider types are `openai` for OpenAI-compatible APIs and `anthropic` for Anthropic-compatible APIs. Toby exposes each provider to supported tools through a per-run `/proxy/<uuid>` URL, so upstream `baseURL` and credential `headers` stay on the host. OpenCode receives these providers translated to `@ai-sdk/openai-compatible` or `@ai-sdk/anthropic`; configured `models` are used verbatim, otherwise Toby queries `/models` on the upstream provider during sandbox startup. Discovery failures log `opencode.model-discovery` and leave only that provider out of generated OpenCode config.
- `sandbox` sets global defaults for sandbox launches. CLI flags override launch config values, launch config values override host config defaults, and host config defaults override built-in defaults.

```yaml
providers:
  local:
    type: openai
    baseURL: https://api.example.com/v1
    headers:
      Authorization: "Bearer {env:EXAMPLE_API_KEY}"
    models:
      example-model: {}
permissions:
  paths:
    ~/shared: allow
    ~/shared/**: allow
```

Managed mount profiles control whether runtime/tool data uses provider-backed storage, private sandbox-home storage, or host bind mounts. The default profile is `default`; `settings.mountProfile` selects another profile for a launch, and `tools.<tool>.mountProfile` can select a profile for one tool. The default backing is `provider`; omitting `backing` is the same as `backing: default`, which resolves to provider-backed storage. The Docker tool is an exception: it explicitly bind-mounts `/var/run/docker.sock` and `~/.docker` instead of using managed mounts.

```yaml
mountProfiles:
  default:
    backing: provider
  host-state:
    backing: host
    hostRoot: ~/.config/toby/mounts/opencode
settings:
  suppressWarnings:
    - mount.host-backing
  autoloadProjectConfig: true # optional; load <project>/.toby.yaml on direct launches
tools:
  opencode:
    mountProfile: host-state
```

Provider-backed tool mounts use IDs like `toby.<mountProfile>.tool.<tool>.<purpose>`; runtime home uses `toby.<mountProfile>.runtime.home.<sandboxName>`. Docker stores provider-backed mounts in Docker volumes. Bubblewrap stores them under `sandbox.runtime.bubblewrap.root` with the provider ID as the directory name, for example `~/.cache/toby/sandboxes/toby.default.tool.opencode.config`. Host-backed mounts use `hostRoot` as if it were `$HOME` for that mount's known subpath. For OpenCode, `hostRoot: ~/.config/toby/mounts/opencode` uses `~/.config/toby/mounts/opencode/.config/opencode` and `~/.config/toby/mounts/opencode/.local/share/opencode`. If `hostRoot` is omitted, host backing uses `$HOME`; relative `hostRoot` values in Toby config resolve from the config file directory. When host backing is enabled for a managed tool mount, Toby emits the `mount.host-backing` warning because running multiple instances against the same host-backed mount can corrupt tool databases. Set `settings.suppressWarnings: true` to suppress all warnings, or set it to a list of warning IDs such as `mount.host-backing`, `opencode.model-discovery`, `project.autoload-disabled`, `project.duplicate`, or `project.missing`. Toby still generates synthetic tool config in all modes.

Set `settings.autoloadProjectConfig: true` in host config to load `<project>/.toby.yaml` during direct launches such as `toby opencode my-app`. If `.toby.yaml` exists and autoload is disabled, Toby emits `project.autoload-disabled`. In autoload mode, the CLI tool and project stay foreground and primary; tools and projects from `.toby.yaml` are added, with duplicate project names skipped after warning.

Example global Docker sandbox defaults:

```yaml
sandbox:
  runtime:
    default: docker
    docker:
      image: node:lts-bookworm
      build:
        context: ~/docker/toby
    bubblewrap:
      root: ~/.cache/toby/sandboxes
```

If no runtime-specific settings are needed, `sandbox.runtime` can also be a string, for example `runtime: docker`.

## Launch Configuration

Use `--config` to launch from a per-run YAML or JSON file. JSON files are parsed with the same YAML parser. If no tool and project are specified on the CLI, the first configured tool is foreground and the first existing configured project is primary. If a CLI tool and project are specified, for example `toby --config myconfig.yaml opencode my-app`, the CLI tool and project stay foreground and primary while the config contributes sandbox settings, extra tools, and extra projects.

```yaml
sandbox:
  name: foo # optional; defaults to the first project name
  runtime:
    default: docker # optional; defaults to the highest-priority available runtime
    docker:
      image: node:lts-bookworm # optional; defaults to node:lts-bookworm
      home: /toby/home # optional; defaults to /toby/home
      projects: /toby/workspace # optional; defaults to /toby/workspace
      build: # optional; build an image before launch
        context: . # defaults to this config file's directory
        dockerfile: Dockerfile.toby # optional; relative to context, defaults to Dockerfile
    bubblewrap:
      root: .toby/sandboxes # optional; relative to this config file
mountProfiles:
  default:
    backing: provider # optional; default, provider, private, or host
  host-state:
    backing: host
    hostRoot: .toby/tool-state # optional; relative to this config file
settings:
  autoUpgrade: true # optional; defaults to false
  suppressWarnings: [mount.host-backing] # optional; true suppresses all warnings
workdir: ~/tmp # optional; defaults to the primary project path inside the sandbox
projects:
  foo:
    primary: true
  baz: # source defaults to $XDG_PROJECTS_DIR/baz
  bar:
    path: ../bar-source # optional source; relative to this config file, leading ~ expands
tools:
  opencode:
    primary: true
    params: ["--model", "anthropic/claude-sonnet-4-5"] # optional; only valid on the primary tool
    mountProfile: host-state # optional; selects a mount profile for this tool
  uv:
  npm:
```

In config-owned launches, the primary project is the working directory. Set `primary: true` when multiple projects are configured; if there is exactly one project, Toby infers it. Configured project paths that do not exist are skipped with the suppressible `project.missing` warning. Duplicate configured project names are skipped with the suppressible `project.duplicate` warning; the same host source path may be mounted multiple times under different project names. In overlay launches, the CLI project remains first and configured projects are additional. In config-owned launches, the primary tool is the foreground tool. Set `primary: true` when multiple tools are configured; if there is exactly one tool, Toby infers it. In overlay launches, the CLI tool is primary and configured tools are additional. `params` is only applied when that tool is the resolved primary tool. Tool names must be registered Toby tools, such as `opencode`, `exec`, `uv`, or `npm`.

Bubblewrap provider-backed mounts are stored under `${XDG_CACHE_HOME:-~/.cache}/toby/sandboxes` by default using provider ID directory names. Configure `sandbox.runtime.bubblewrap.root` to use a different host directory. Docker provider-backed mounts use named Docker volumes instead.

Path values in launch config expand a leading `~` to the user's home directory. Toby does not otherwise clean, canonicalize, or resolve symlinks as part of config path expansion.

`workdir` is passed to the selected sandbox runtime after leading `~` expansion to the sandbox home and is not otherwise resolved or validated by Toby. If omitted, Toby uses the first configured project's sandbox path.

Toby parses all arguments before the first `--`; command arguments must come after it. Everything after that first `--`, including later `--` values, is appended to the primary tool's configured `params`:

```sh
toby --config myconfig.yaml -- --additional-param value
```

Use `exec` as the primary tool to run arbitrary sandbox commands from `params` or from CLI arguments.

Configured project `path` values are host source directories. If a project is a string or an object with only `name`, the host source defaults to the host `$XDG_PROJECTS_DIR/<name>`. Explicit relative `path` values resolve from the launch config file directory, absolute paths are used as-is, and leading `~` expands to the host home. Each project appears inside the sandbox under the selected runtime's project root: `/toby/workspace/<name>` for Docker and `$XDG_PROJECTS_DIR/<name>` for Bubblewrap.

```yaml
projects:
  foo:
tools:
  exec:
    primary: true
    params: ["npm", "test"]
  npm:
```

```sh
toby --config myconfig.yaml -- -- --watch
```

This runs `npm test -- --watch` in `/toby/workspace/foo` with Docker, or `$XDG_PROJECTS_DIR/foo` with Bubblewrap.

Sandbox paths are runtime-specific. Docker uses `/toby`: `$HOME` is `/toby/home`, projects mount under `/toby/workspace`, generated context lives under `/toby/context`, and the helper binary is downloaded to `/toby/bin/toby`. Docker persists only `$HOME` in a named volume. Bubblewrap keeps normal `$HOME` and `$XDG_PROJECTS_DIR` paths, with generated context and the helper binary under `${XDG_RUNTIME_DIR:-/run/user/<uid>}/toby`. Toby does not construct startup environment variables from host values; it explicitly sets only calculated `HOME` plus `TOBY_CONTROL_HOST` and `TOBY_CONTROL_TOKEN` for the sandbox manager. The Docker image is responsible for containing the tools needed by the selected Toby tools; use `sandbox.runtime.docker.image` when a custom image is required. Set `sandbox.runtime.docker.build.context` to build an image from a Dockerfile. Relative build contexts resolve from the config file directory; relative `dockerfile` values resolve from the build context and default to `Dockerfile`. If `image` is set, Toby uses it when it already exists locally and builds it otherwise. If `image` is omitted, Toby always runs `docker build` and uses the resulting image ID.

Docker `sandbox.runtime.docker.home`, `sandbox.runtime.docker.projects`, and `workdir` values are sandbox-visible paths. A leading `~` expands to the Docker sandbox home.

## Common Commands

```sh
toby opencode <env>
toby claude <env>
toby codex <env>
toby exec <env> -- <command arguments>
```

Useful flags:

- `--project <dir>` selects a project directory under `XDG_PROJECTS_DIR`.
- `--sandbox-runtime <bubblewrap|docker>` selects the sandbox runtime.
- `--sandbox-image <image>` selects the Docker image for Docker-backed direct launches.
- `--config <file>` launches from a YAML or JSON launch configuration.
- `--install` installs the selected tool and exits.
- `--upgrade` reinstalls the selected tool, then launches it.

## MCP

Toby automatically exposes a sandbox-only MCP server to supported tools launched through `toby <client>`. The built-in server is registered as a per-run `/proxy/<uuid>` target, like configured remote MCP servers, and provides `git.commit`, `git.fetch`, `git.push`, `git.rebase`, and `git.tag` for repositories already visible in the sandbox. For OpenCode, Claude Code, Copilot, and Grok, Toby injects this server through synthetic tool configuration generated under the context directory. Grok discovers that generated config through a `~/.grok/managed_config.toml` symlink. Codex receives Toby MCP through launch-time `-c` config overrides instead of a generated profile file.

The sandbox manager uses `TOBY_CONTROL_HOST=host:port` and `TOBY_CONTROL_TOKEN` to connect back to the host and download the helper binary. Launched sandbox commands do not receive those control variables. `HOME` remains available to commands. MCP proxy URLs use the per-run control host and proxy UUID.

Configured remote MCP servers are exposed through per-run HTTP proxy URLs with their original configured names. For example, a `mcps.docs` entry using `type: remote` and `url: https://example.com/mcp` is rendered to supported tools as a remote MCP pointing at `http://<control-host>/proxy/<uuid>`. Toby opens the upstream connection from the host process and applies the configured `headers` there, resolving any `{env:VAR}` and `{file:path}` substitutions on the host so credentials never enter the sandbox. Local MCP entries are rendered as local commands for tools that support them.

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
projects:
  my-app:
tools:
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
- [Control protocol schema](docs/toby-control-openapi.yaml) — JSON-RPC over WebSocket OpenAPI spec.
