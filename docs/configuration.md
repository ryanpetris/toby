# Configuration

Toby has three layers of configuration, applied in increasing precedence:

1. **Built-in defaults.**
2. **Host config** — `$XDG_CONFIG_HOME/toby/config.{json,jsonc,yaml,yml}`.
   Global settings and secrets that stay on the host.
3. **Launch config** — a per-run file passed with `--config`, or a project's
   `.toby.yaml` loaded by autoload. Defines the projects, tools, and sandbox for
   one launch.
4. **CLI flags** — highest precedence for the fields they cover.

For sandbox defaults the rule is: **CLI flags override launch config, launch
config overrides host config defaults, host config defaults override built-in
defaults.**

This page is the field-level reference. See [tools.md](tools.md) for what each
tool does with the generated config, and [examples.md](examples.md) for
worked examples.

## Paths and environment

Toby follows XDG conventions (`internal/config/paths.go`):

| Variable | Default | Used for |
| --- | --- | --- |
| `XDG_PROJECTS_DIR` | `~/Projects` | Host project root used to resolve default project sources. |
| `XDG_CONFIG_HOME` | `~/.config` | Host config dir is `$XDG_CONFIG_HOME/toby`. |
| `XDG_CACHE_HOME` | `~/.cache` | Bubblewrap homes under `$XDG_CACHE_HOME/toby/sandboxes`. |

Sandbox paths are runtime-specific. Docker uses `/toby`: `/toby/home` is
`$HOME`, `/toby/workspace` contains mounted projects, `/toby/bin` contains the
helper binary, and `/toby/context` contains generated configuration and
instructions. Bubblewrap keeps `$HOME` and `$XDG_PROJECTS_DIR` at their normal
paths and stores Toby internals under `${XDG_RUNTIME_DIR:-/run/user/<uid>}/toby`.
Toby does not construct startup environment variables from host values. It sets
calculated `HOME` for the selected sandbox paths and otherwise lets the runtime
supply the sandbox environment.

The sandbox bootstrap and manager also receive `TOBY_CONTROL_HOST=host:port` and
`TOBY_CONTROL_TOKEN` to reach the host control server. Launched sandbox commands
do not receive those control variables.

Path expansion: a leading `~` or `~/` expands to the relevant home directory.
Toby does not otherwise clean, canonicalize, or resolve symlinks during config
path expansion.

## Host config

Host config is loaded from `$XDG_CONFIG_HOME/toby/` in this order, and any files
that exist are deep-merged in order (`internal/config/file`,
`internal/config/toby`):

1. `config.json`
2. `config.jsonc` (JSON with comments and trailing commas)
3. `config.yaml`
4. `config.yml`

JSON files are parsed with the same parser used for YAML's normalized form;
JSONC comments and trailing commas are stripped first. Empty/whitespace-only
files are skipped. On deep merge, nested objects merge recursively, the
`instructions` array is de-duplicated and appended, and other arrays and scalars
are last-write-wins.

Toby config is its **own** format — it is not OpenCode config, though some
nested shapes intentionally mirror OpenCode for convenience. The only supported
top-level keys are `instructions`, `mcp`, `permission`, `provider`, and
`sandbox`. **Any other top-level key fails config loading.**

### `instructions`

An array of host instruction file paths or glob patterns. Relative paths
resolve from `$XDG_CONFIG_HOME/toby`; a leading `~` expands to the host home.
During context init, matching files are copied into
the generated context directory using the source basename. If two included
files share a basename, later files get a short random suffix before the
extension (e.g. `foobar.1a2b3c.md`). Instruction contents are combined and
delivered to each tool through that tool's native instruction mechanism (see
[tools.md](tools.md)).

```yaml
instructions:
  - house-style.md
  - ~/notes/review-checklist.md
  - prompts/*.md
```

### `mcp`

A map of MCP server name to definition. Entries are rendered into supported
synthetic tool configs under the generated context directory; Toby's own MCP server is
always injected as `toby` after host config is merged.

| Field | Type | Notes |
| --- | --- | --- |
| `type` | `local` \| `remote` | Selects local-command vs remote-URL. |
| `enabled` | bool | Defaults to `true`. |
| `command` | string \| array | For `type: local`. First element is the command. |
| `url` | string | For `type: remote`. The upstream MCP URL. |
| `headers` | object | Headers for remote servers. Values are a string or string array and may use `{env:VAR}` / `{file:path}` substitution (resolved on the host). |

Remote entries are exposed to tools through a per-run
`http://<control-host>/proxy/<uuid>` URL; Toby opens the upstream connection
from the host, resolves any `{env:VAR}` / `{file:path}` substitutions in
`headers`, and applies the resolved headers there, so the upstream URL and
credentials stay on the host. Local entries are rendered as local commands for
tools that support them.

```yaml
mcp:
  docs:
    type: remote
    url: https://example.com/mcp
    headers:
      Authorization: "Bearer {env:DOCS_TOKEN}"
  local-fs:
    type: local
    command: ["my-mcp-server", "--root", "/srv"]
```

### `permission`

`permission.paths` maps host path patterns to permission modes (e.g. `allow`)
rendered into supported tool configs. A leading `~` expands to the host home.

```yaml
permission:
  paths:
    ~/shared: allow
    ~/shared/**: allow
```

### `provider`

A map of provider name to declaration. Supported `type` values are `openai`
(OpenAI-compatible) and `anthropic` (Anthropic-compatible). Toby keeps upstream
`baseURL` and credential `headers` on the host and exposes each provider to
tools through `http://<control-host>/proxy/<uuid>`.

| Field | Type | Notes |
| --- | --- | --- |
| `type` | `openai` \| `anthropic` | Required. |
| `name` | string | Display name (optional). |
| `baseURL` | string | Required. Upstream API base URL. |
| `headers` | object | Credential/HTTP headers, kept on host. Supports `{env:VAR}` / `{file:path}` substitution. |
| `models` | object | Model entries; used verbatim when present. |

For OpenCode, providers are translated to `@ai-sdk/openai-compatible` or
`@ai-sdk/anthropic`. If `models` is omitted, Toby queries the upstream
`/models` endpoint during sandbox startup (`internal/providers/openai`).
Discovery failures emit the `opencode.model-discovery` warning and omit only
the failed provider from generated OpenCode config.

```yaml
provider:
  local:
    type: openai
    baseURL: https://api.example.com/v1
    headers:
      Authorization: "Bearer {env:EXAMPLE_API_KEY}"
    models:
      example-model: {}
```

### `sandbox`

Global sandbox defaults. Every field can be overridden per launch.

```yaml
sandbox:
  runtime:
    default: docker          # docker | bubblewrap; defaults to highest-priority available
    docker:
      image: node:lts-bookworm
      build:
        context: ~/docker/toby
    bubblewrap:
      root: ~/.cache/toby/sandboxes
  tools:
    default:
      state: private         # private | host
    opencode:
      state: host
      stateRoot: ~/.config/toby/tool-state/opencode
    docker:
      state: private
  suppressWarnings:
    - tool.host-state
  autoloadProjectConfig: true
```

- `sandbox.runtime` may be a string shorthand (`runtime: docker`) when no
  runtime-specific options are needed.
- `sandbox.runtime.docker.home` and `sandbox.runtime.docker.projects` are
  sandbox-visible paths; if set, they must stay under `/toby`.
- `sandbox.runtime.bubblewrap.root` and relative `stateRoot` values in host
  config resolve from the Toby config file directory.
- `sandbox.autoloadProjectConfig: true` loads `<project>/.toby.yaml` on direct
  launches (see [Autoload](#autoload)).

## Tool state

`sandbox.tools` controls where each tool keeps its own state.

- `state: private` (default) — the tool uses the private sandbox home; nothing
  on the host is bind-mounted.
- `state: host` — Toby bind-mounts the tool's known state paths from
  `stateRoot`, which is treated like `$HOME` for that tool. If `stateRoot` is
  omitted, host state uses the actual `$HOME`.

The **Docker** tool is the exception: it defaults to `host` state (so it sees
your real Docker config) unless `docker.state: private` is set. Its
`/var/run/docker.sock` bind stays enabled even when its state is private.

Enabling host state for a **non-Docker** tool emits the suppressible
`tool.host-state` warning, because concurrent sandboxes sharing one tool's
state directory can corrupt that tool's databases.

Example resolution: OpenCode with `stateRoot: ~/.config/toby/tool-state/opencode`
uses `~/.config/toby/tool-state/opencode/.config/opencode` and
`~/.config/toby/tool-state/opencode/.local/share/opencode` as host sources.
Synthetic Toby config is generated in **both** private and host modes.

## Warnings

All warnings are suppressible via `sandbox.suppressWarnings`: set it to `true`
to suppress everything, or to a list of IDs.

| ID | Meaning |
| --- | --- |
| `tool.host-state` | Host state enabled for a non-Docker tool. |
| `opencode.model-discovery` | OpenCode provider model discovery failed. |
| `project.autoload-disabled` | `.toby.yaml` present but autoload is off. |
| `project.duplicate` | Duplicate configured project name skipped. |
| `project.missing` | Configured project path does not exist. |

## Launch config

Pass a per-run launch file with `--config <file>` (YAML, or JSON parsed by the
same YAML parser). A launch file describes one launch: its sandbox, projects,
tools, and working directory.

```yaml
sandbox:
  name: foo              # optional; defaults to the first project name
  autoUpgrade: true      # optional; defaults to false
  runtime:
    default: docker      # optional; defaults to highest-priority available runtime
    docker:
      image: node:lts-bookworm  # optional; defaults to node:lts-bookworm
      home: /toby/home          # optional; defaults to /toby/home
      projects: /toby/workspace # optional; defaults to /toby/workspace
      build:                    # optional; build an image before launch
        context: .              # defaults to this config file's directory
        dockerfile: Dockerfile.toby  # optional; relative to context, defaults to Dockerfile
    bubblewrap:
      root: .toby/sandboxes     # optional; relative to this config file
  tools:
    default:
      state: private     # optional; private or host
    claude:
      state: host        # optional; overrides default for this tool
      stateRoot: .toby/claude-state  # optional; relative to this config file
  suppressWarnings: [tool.host-state]  # optional; true suppresses all warnings
workdir: ~/tmp           # optional; defaults to the primary project path in the sandbox
projects:
  - foo
  - name: baz            # equivalent to `baz`; source defaults to $XDG_PROJECTS_DIR/baz
  - name: bar
    path: ../bar-source  # optional source; relative to this config file, leading ~ expands
tools:
  - name: opencode
    params: ["--model", "anthropic/claude-sonnet-4-5"]  # only valid on the first tool
  - uv
  - npm
```

### `projects`

Each entry is a string or `{name, path?}` object.

- The project appears inside the sandbox under the selected runtime's project
  root: `/toby/workspace/<name>` for Docker and `$XDG_PROJECTS_DIR/<name>` for
  Bubblewrap, regardless of where its host source lives.
- `path` is the host **source**. If omitted, it defaults to
  `$XDG_PROJECTS_DIR/<name>`. Explicit relative paths resolve from the config
  file directory; absolute paths are used as-is; leading `~` expands to the host
  home.
- Missing project paths are skipped with `project.missing`. Duplicate project
  names are skipped with `project.duplicate` (the same source may be mounted
  under different names).
- Toby Git and MCP repository names use the configured project **names**, not
  the host source paths.

### `tools`

Each entry is a string or `{name, params?}` object. Names must be registered
Toby tools (see [tools.md](tools.md)). `params` is only honored on the **first**
tool in a config-owned launch. In a config-owned launch the first tool is the
launch (foreground) tool and later tools are installed and made available in
order.

### `workdir`

Passed to the runtime after leading-`~` expansion to the sandbox home; not
otherwise resolved or validated. If omitted, the working directory is the first
configured project's sandbox path.

### Config-owned vs overlay launches

- **Config-owned launch:** `toby --config foo.yaml` (no CLI tool/project). The
  first tool is the foreground tool; the first existing project is the working
  directory.
- **Overlay launch:** `toby --config foo.yaml opencode my-app`. The CLI tool and
  project stay foreground and primary; the config contributes sandbox settings
  plus *additional* tools and projects. Tools are de-duplicated against the CLI
  tool.

### Argument passing through `--`

Toby parses all arguments before the first `--`; command arguments come after
it. Everything after that first `--`, including later `--` tokens, is appended
to the first tool's configured `params`:

```sh
toby --config foo.yaml -- --additional-param value
```

With `exec` as the first tool you can run arbitrary commands. For example:

```yaml
projects: [foo]
tools:
  - name: exec
    params: ["npm", "test"]
  - npm
```

```sh
toby --config foo.yaml -- -- --watch
```

runs `npm test -- --watch` in `/toby/workspace/foo` with Docker, or
`$XDG_PROJECTS_DIR/foo` with Bubblewrap.

## Autoload

Set `sandbox.autoloadProjectConfig: true` in **host** config to make direct
launches (e.g. `toby opencode my-app`) load `<project>/.toby.yaml` if present.
In autoload mode the CLI tool and project remain foreground and primary, and the
tools/projects from `.toby.yaml` are added (duplicate project names are skipped
after warning). If `.toby.yaml` exists but autoload is disabled, Toby emits
`project.autoload-disabled`.

The repository's own `.toby.yaml` is a minimal example:

```yaml
sandbox:
  runtime:
    default: docker
    docker:
      build:
        context: support/toby
tools:
  - docker
```
