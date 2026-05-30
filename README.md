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

- Linux with Bubblewrap available at `/usr/bin/bwrap` for the default runtime, or Docker for Docker-backed sandboxes.
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

- `mcp` entries are added to supported generated tool configs, alongside Toby's built-in MCP server.
- `instructions` entries are host instruction file paths. Relative paths resolve from the Toby config directory. Toby copies them into `$XDG_RUNTIME_DIR/toby/context/instructions/` using the source filename, adding a short random suffix before the extension if two files share a filename.
- `provider` entries use OpenCode's provider schema and are currently applied to OpenCode only. If a provider includes `models`, Toby uses those models verbatim. For OpenAI-compatible providers without `models`, Toby queries the provider at sandbox startup; if discovery fails, Toby logs a warning and leaves that provider out of the generated OpenCode config.
- `sandbox` sets global defaults for sandbox launches. CLI flags override launch config values, launch config values override host config defaults, and host config defaults override built-in defaults.

Example global Docker sandbox defaults:

```yaml
sandbox:
  runtime: docker
  docker:
    image: node:lts-bookworm
```

## Launch Configuration

Use `--config` to launch from a per-run YAML or JSON file instead of specifying the tool and project on the command line. JSON files are parsed with the same YAML parser.

```yaml
sandbox:
  name: foo # optional; defaults to the first project name
  autoUpgrade: true # optional; defaults to false
  runtime: docker # optional; defaults to bubblewrap
  docker:
    image: node:lts-bookworm # optional; defaults to node:lts-bookworm
    home: /home/toby # optional; defaults to your host $HOME path
    projects: /workspace # optional; defaults to your host XDG_PROJECTS_DIR path
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

The first project is the working directory. The first tool is the launch tool, and later tools are installed and made available in order. Tool entries may be strings or objects with `name`; `params` is only allowed on the first tool. Tool names must be registered Toby tools, such as `opencode`, `exec`, `uv`, or `npm`.

Path values in launch config expand a leading `~` to the user's home directory. Toby does not otherwise clean, canonicalize, or resolve symlinks as part of config path expansion.

`workdir` is passed to the selected sandbox runtime after leading `~` expansion to the sandbox home and is not otherwise resolved or validated by Toby. If omitted, Toby uses the first configured project's sandbox path.

Command arguments are still passed after `--` and are appended to the first tool's configured `params`:

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

For Docker sandboxes, projects are mounted under the same path as host `XDG_PROJECTS_DIR` by default, so `~/Projects/foo` remains visible at that path. Docker uses the same `$HOME` path as the host by default, backed by a named Docker volume such as `toby-home-foo`. The Docker image is responsible for containing the tools needed by the selected Toby tools; use `sandbox.docker.image` when a custom image is required.

Docker `sandbox.docker.home`, `sandbox.docker.projects`, and `workdir` values are sandbox-visible paths. A leading `~` expands to the Docker sandbox home.

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

Toby automatically exposes a sandbox-only `toby sandbox mcp` server to supported tools launched through `toby <client>`. The server uses a private Unix socket at `$XDG_RUNTIME_DIR/toby/sandbox.sock` inside the sandbox and provides `git.commit`, `git.fetch`, `git.push`, `git.rebase`, and `git.tag` for repositories already visible in the sandbox.

Inside the sandbox, Toby bind-mounts the same binary as `toby` and enables hidden `toby sandbox ...` commands. On the host these commands are hidden from help but still registered for diagnostics.

## More Docs

- [Sandbox and integration details](docs/sandbox.md)
