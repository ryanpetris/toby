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

Toby reads host configuration from `${XDG_CONFIG_HOME:-~/.config}/toby/config.json`, `config.jsonc`, `config.yaml`, and `config.yml`; if `XDG_CONFIG_HOME` is unset, Toby also accepts `XDG_CONFIG_DIR` before falling back to `~/.config`. If more than one file exists, Toby deep merges them in that order.

Toby config is its own format. Supported top-level keys are `instructions`, `mcp`, `permission`, `provider`, and `sandbox`; unsupported top-level keys fail config loading. Some nested shapes intentionally mirror OpenCode for convenience:

- `mcp` entries are added to supported generated tool configs, alongside Toby's built-in MCP server. Generated tool config lives under `/tmp/toby/context` inside the sandbox and does not modify the tools' normal config files.
- `instructions` entries are host instruction file paths. Relative paths resolve from the Toby config directory. Toby copies them into `/tmp/toby/context/instructions/` inside the sandbox using the source filename, adding a short random suffix before the extension if two files share a filename.
- `provider` entries use OpenCode's provider schema and are currently applied to OpenCode only. If a provider includes `models`, Toby uses those models verbatim. For OpenAI-compatible providers without `models`, Toby queries the provider at sandbox startup; if discovery fails, Toby logs the `opencode.model-discovery` warning and leaves that provider out of the generated OpenCode config.
- `sandbox` sets global defaults for sandbox launches. CLI flags override launch config values, launch config values override host config defaults, and host config defaults override built-in defaults.

Tool state controls whether selected tools use per-environment private state or bind mount the host's tool state directories. It defaults to `private`; the Docker tool is the exception and uses host state unless `docker.state` is explicitly set to `private`. The Docker socket is still passed to the Docker tool when its state is private.

```yaml
sandbox:
  tools:
    default:
      state: private
    opencode:
      state: host
      stateRoot: ~/.config/toby/tool-state/opencode
    docker:
      state: private
  suppressWarnings:
    - tool.host-state
  autoloadProjectConfig: true # optional; load <project>/.toby.yaml on direct launches
```

When host state is enabled, `stateRoot` is treated like `$HOME` for that tool's known state paths. For OpenCode, `stateRoot: ~/.config/toby/tool-state/opencode` uses `~/.config/toby/tool-state/opencode/.config/opencode` and `~/.config/toby/tool-state/opencode/.local/share/opencode`. If `stateRoot` is omitted, host state uses `$HOME`; relative `stateRoot` values in Toby config resolve from the config file directory. When host state is enabled for a non-Docker tool, Toby emits the `tool.host-state` warning because running multiple instances against the same host tool state can corrupt tool databases. Set `sandbox.suppressWarnings: true` to suppress all warnings, or set it to a list of warning IDs such as `tool.host-state`, `opencode.model-discovery`, `project.autoload-disabled`, or `project.missing`. Toby still generates synthetic tool config in both modes.

Set `sandbox.autoloadProjectConfig: true` in host config to load `<project>/.toby.yaml` during direct launches such as `toby opencode my-app`. If `.toby.yaml` exists and autoload is disabled, Toby emits `project.autoload-disabled`. In autoload mode, the CLI tool and project stay foreground and primary; tools and projects from `.toby.yaml` are added and deduplicated.

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
  autoUpgrade: true # optional; defaults to false
  runtime:
    default: docker # optional; defaults to the highest-priority available runtime
    docker:
      image: node:lts-bookworm # optional; defaults to node:lts-bookworm
      home: /home/toby # optional; defaults to your host $HOME path
      projects: /workspace # optional; defaults to your host XDG_PROJECTS_DIR path
      build: # optional; build an image before launch
        context: . # defaults to this config file's directory
        dockerfile: Dockerfile.toby # optional; relative to context, defaults to Dockerfile
    bubblewrap:
      root: .toby/sandboxes # optional; relative to this config file
  tools:
    default:
      state: private # optional; private or host
    claude:
      state: host # optional; overrides default for this tool
      stateRoot: .toby/claude-state # optional; relative to this config file
  suppressWarnings: [tool.host-state] # optional; true suppresses all warnings
workdir: ~/tmp # optional; defaults to the primary project path inside the sandbox
projects:
  - foo
  - name: bar
    path: ../bar-source # relative to this config file, defaults to "."; leading ~ expands
tools:
  - name: opencode
    params: ["--model", "anthropic/claude-sonnet-4-5"] # optional; only valid on the first tool
  - uv
  - npm
```

In config-owned launches, the first existing project is the working directory. Configured project paths that do not exist are skipped with the suppressible `project.missing` warning. In overlay launches, the CLI project remains first and configured projects are additional. In config-owned launches, the first tool is the launch tool, and later tools are installed and made available in order. In overlay launches, configured tools are additional and are deduplicated with the CLI tool. Tool entries may be strings or objects with `name`; `params` is only applied to the first tool in config-owned launches. Tool names must be registered Toby tools, such as `opencode`, `exec`, `uv`, or `npm`.

Bubblewrap private homes are stored under `${XDG_CACHE_HOME:-~/.cache}/toby/sandboxes` by default. Configure `sandbox.runtime.bubblewrap.root` to use a different host directory. Docker homes use named Docker volumes instead.

Path values in launch config expand a leading `~` to the user's home directory. Toby does not otherwise clean, canonicalize, or resolve symlinks as part of config path expansion.

`workdir` is passed to the selected sandbox runtime after leading `~` expansion to the sandbox home and is not otherwise resolved or validated by Toby. If omitted, Toby uses the first configured project's sandbox path.

Toby parses all arguments before the first `--`; command arguments must come after it. Everything after that first `--`, including later `--` values, is appended to the first tool's configured `params`:

```sh
toby --config myconfig.yaml -- --additional-param value
```

Use `exec` as the primary tool to run arbitrary sandbox commands from `params` or from CLI arguments.

Configured project `path` values are host source directories. Each project always appears inside the sandbox under `$XDG_PROJECTS_DIR/<name>`, even when the source directory is elsewhere. For example, `name: baz` with `path: /foo/bar` is mounted as `$XDG_PROJECTS_DIR/baz` in the sandbox.

```yaml
projects:
  - foo
tools:
  - name: exec
    params: ["npm", "test"]
  - npm
```

```sh
toby --config myconfig.yaml -- -- --watch
```

This runs `npm test -- --watch` in `$XDG_PROJECTS_DIR/foo`.

For Docker sandboxes, projects are mounted under the same path as host `XDG_PROJECTS_DIR` by default, so `~/Projects/foo` remains visible at that path. Docker uses the same `$HOME` path as the host by default, backed by a named Docker volume such as `toby-home-foo`. The Docker image is responsible for containing the tools needed by the selected Toby tools; use `sandbox.runtime.docker.image` when a custom image is required. Set `sandbox.runtime.docker.build.context` to build an image from a Dockerfile. Relative build contexts resolve from the config file directory; relative `dockerfile` values resolve from the build context and default to `Dockerfile`. If `image` is set, Toby uses it when it already exists locally and builds it otherwise. If `image` is omitted, Toby always runs `docker build` and uses the resulting image ID. At startup, the sandbox downloads the Toby helper from the host control server into `/tmp/toby/bin/toby`.

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
- `--tool-state <private|host>` selects the default tool state source for direct launches.
- `--tool-state-root <dir>` selects the default host state root for direct launches with host state; relative paths resolve from the project root.
- `--config <file>` launches from a YAML or JSON launch configuration.
- `--install` installs the selected tool and exits.
- `--upgrade` reinstalls the selected tool, then launches it.

## MCP

Toby automatically exposes a sandbox-only `toby sandbox mcp` server to supported tools launched through `toby <client>`. For OpenCode, Claude Code, Copilot, and Grok, Toby injects this server through synthetic tool configuration generated under `/tmp/toby/context`. Grok discovers that generated config through a `~/.grok/managed_config.toml` symlink. Codex receives Toby MCP through launch-time `-c` config overrides instead of a generated profile file. The server uses Toby's authenticated WebSocket control connection and provides `git.commit`, `git.fetch`, `git.push`, `git.rebase`, and `git.tag` for repositories already visible in the sandbox.

Toby does not write generated config into regular tool config files such as `~/.codex`, `~/.copilot`, or `~/.grok/config.toml`; Grok's `managed_config.toml` symlink points back to `/tmp/toby/context/grok/config.toml`. Tool-specific instruction injection is also session-scoped: Copilot receives a generated `AGENTS.md` directory through `COPILOT_CUSTOM_INSTRUCTIONS_DIRS`, Grok receives combined rules through `--rules`, and Codex receives combined developer instructions through `-c developer_instructions=...`.

Inside the sandbox, Toby downloads the sandbox-facing Toby binary as `toby` and enables hidden `toby sandbox ...` commands. On the host these commands are hidden from help but still registered for diagnostics.

## More Docs

- [Sandbox and integration details](docs/sandbox.md)
