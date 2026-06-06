# Configuration

Toby has three layers of configuration, applied in increasing precedence:

1. **Built-in defaults.**
2. **Host config** — `$XDG_CONFIG_HOME/toby/config.{json,yaml,yml}`.
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

Toby follows XDG conventions (`config/paths.go`):

| Variable | Default | Used for |
| --- | --- | --- |
| `XDG_PROJECTS_DIR` | `~/Projects` | Host project root used to resolve default project sources. |
| `XDG_CONFIG_HOME` | `~/.config` | Host config dir is `$XDG_CONFIG_HOME/toby`. |

The sandbox runs in a container under `/toby`: `/toby/home` is `$HOME`,
`/toby/workspace` contains mounted projects, `/toby/bin` contains the helper
binary, and `/toby/context` contains generated configuration and instructions.
Toby does not construct startup environment variables from host values. It sets
calculated `HOME` for the sandbox paths and `TOBY_SANDBOX=1` for the manager,
passes host `TERM` to the container when it is set, and otherwise lets the
container supply the sandbox environment. Per-command environment is injected
into each `docker exec`.

The host reaches the sandbox manager over a gRPC link carried on the container's
stdio, so there is no control host or token to pass in.

Path expansion: a leading `~` or `~/` expands to the relevant home directory.
Toby does not otherwise clean, canonicalize, or resolve symlinks during config
path expansion.

## Host config

Host config is loaded from `$XDG_CONFIG_HOME/toby/` in this order, and any files
that exist are deep-merged in order (`config/file`, `internal/config/app`):

1. `config.json`
2. `config.yaml`
3. `config.yml`

JSON and YAML are both decoded strictly: unknown fields are rejected (use YAML
if you want comments). Empty/whitespace-only files are skipped. On deep merge,
nested objects merge recursively, the `instruction` array is de-duplicated and
appended, and other arrays and scalars are last-write-wins.

Toby config is its **own** format — it is not OpenCode config, though some
nested shapes intentionally mirror OpenCode for convenience. The only supported
top-level keys are `instruction`, `mcp`, `permission`, `provider`,
`settings`, `tool`, and `container`. **Any other top-level key fails
config loading.**

### `instruction`

An array of host instruction file paths or glob patterns. Relative paths
resolve from `$XDG_CONFIG_HOME/toby`; a leading `~` expands to the host home.
During context init, matching files are copied into
the generated context directory using the source basename. If two included
files share a basename, later files get a short random suffix before the
extension (e.g. `foobar.1a2b3c.md`). Instruction contents are combined and
delivered to each tool through that tool's native instruction mechanism (see
[tools.md](tools.md)).

```yaml
instruction:
  - house-style.md
  - ~/notes/review-checklist.md
  - prompts/*.md
```

### `mcp`

`mcp` collects all MCP configuration. `mcp.server` is a map of MCP server name
to definition. Entries are
Toby-managed proxy targets
rendered into supported synthetic tool configs under the generated context
directory; Toby's own MCP server is always injected as `toby` after host config
is merged. Configure non-proxied tool-native MCPs in the tool's own config.

| Field | Type | Notes |
| --- | --- | --- |
| `type` | `local` \| `remote` | Selects local sidecar vs remote URL. |
| `enabled` | bool | Defaults to `true`. |
| `command` | string \| array | For `type: local`. First element is the command. |
| `transport` | `stdio` \| `http` | For `type: local`; defaults to `stdio`. |
| `image` | string | For `type: local`; the sidecar image. Defaults to the main sandbox image, then Toby's built-in image. |
| `port` | number | Required for local `transport: http`; the container port. |
| `path` | string | URL path for local HTTP MCPs; defaults to `/`. |
| `url` | string | For `type: remote`. The upstream MCP URL. |
| `headers` | object | Headers for remote servers. Values are a string or string array and may use `{env:VAR}` / `{file:path}` substitution (resolved on the host). |

Remote entries are exposed to tools through a per-run
`http://<control-host>/proxy/<uuid>` URL; Toby opens the upstream connection
from the host, resolves any `{env:VAR}` / `{file:path}` substitutions in
`headers`, and applies the resolved headers there, so the upstream URL and
credentials stay on the host. Local entries are started asynchronously as
managerless sidecars and exposed through the same proxy URL shape.

```yaml
mcp:
  server:
    docs:
      type: remote
      url: https://example.com/mcp
      headers:
        Authorization: "Bearer {env:DOCS_TOKEN}"
    local-fs:
      type: local
      transport: stdio
      image: ghcr.io/acme/mcp-node:latest   # optional per-server image override
      command: ["my-mcp-server", "--root", "/srv"]
    local-http:
      type: local
      transport: http
      command: ["my-http-mcp", "--host", "0.0.0.0", "--port", "3000"]
      port: 3000
      path: /mcp
```

### `permission`

`permission.paths` maps path patterns to permission modes (e.g. `allow`)
rendered into supported tool configs. A leading `~` expands to the host home.

Toby injects default permissions for the sandbox projects root, `/tmp`, and the
common sandbox `$HOME` cache/state directories used by Go, npm, and pip (`~/go`
and `~/.cache`). Configured entries override the generated defaults for the same
path, so an explicit `deny` removes an injected default.

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
`/models` endpoint during sandbox startup (`providers/openai`, `providers/anthropic`).
Discovery failures emit the `provider.model-discovery` warning and omit only
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

### `container` and `settings`

Global container defaults live under `container`. Warning/autoload settings
and the mount-profile selection are top-level `settings` keys. Every field can be
overridden per launch.

```yaml
container:
  image: mcr.microsoft.com/devcontainers/javascript-node:24-bookworm
  build:
    context: ~/docker/toby
settings:
  mountProfile: default      # namespaces persistent volumes; defaults to default
  autoloadProjectConfig: true
  debug: false
  yolo: false
tool:
  opencode:
    mountProfile: work       # namespaces this tool's volumes separately
```

- A reachable Docker socket is required. Podman and remote daemons work through
  the standard `DOCKER_HOST` environment variable (e.g.
  `DOCKER_HOST=unix:///run/user/1000/podman/podman.sock`); there is no runtime
  selection in Toby.
- The in-container layout is fixed (`/toby/home`, `/toby/workspace`, `/toby/bin`,
  `/toby/context`); it is not configurable.
- Local MCP entries can set a per-server `image`. MCP sidecar image precedence is
  per-MCP `image`, then the main sandbox image, then Toby's built-in image.
- Publishing sandbox ports to the host (`container.ports`) is launch-only — set it
  in the project `.toby.yaml` or with `--publish`/`-p`, not in the host config.
- `settings.autoloadProjectConfig: true` loads `<project>/.toby.yaml` on direct
  launches (see [Autoload](#autoload)).
- `settings.debug: true` enables debug mode. In sandbox and MCP sidecar
  containers, Toby stops containers on exit but leaves them on the host (rather
  than removing them) so they can be inspected; containers are still created fresh
  and never reused. Toby's MCP `toby://session/...` resources include host paths,
  Docker volume names, and local MCP host ports only in debug mode. Provider and
  MCP headers, URLs, commands, argv, and environment values are never exposed,
  even in debug mode. A project `.toby.yaml` with `settings.debug: false` overrides a
  global `settings.debug: true`; `--debug` overrides config for the launch.
- `settings.yolo: true` launches the AI tool with its permission-bypass flag so it
  no longer prompts to approve actions: Claude with `--dangerously-skip-permissions`,
  Copilot with `--allow-all-tools`, and Codex with
  `--dangerously-bypass-approvals-and-sandbox`. Grok, OpenCode, and Spec Kit have no
  equivalent flag and are unaffected. Defaults to `false`. A project `.toby.yaml`
  with `settings.yolo: false` overrides a global `settings.yolo: true`, mirroring
  `settings.debug` precedence. The `--yolo` launch flag overrides config for a
  single launch, the same way `--debug` does.

## Managed Mounts

Persistent runtime and tool state lives in container-native Docker named volumes.
A mount *profile* is just a namespace label on those volume names, so different
profiles keep separate sets of state. `settings.mountProfile` selects the profile
for a launch (default `default`), and a host or launch `tool.<tool>.mountProfile`
selects a different profile for one tool.

- A tool requests a mount by `type`/`name`/`purpose` at a container path
  (`~/…` expands to the container `$HOME`, `/toby/home`). Toby never bind-mounts
  the user's host tool configuration.
- Each request resolves to a lazy Docker volume named
  `toby.<mountProfile>.<type>.<name>.<purpose>`, managed by Docker. Runtime home
  uses `toby.<mountProfile>.runtime.home.<sandboxName>`.
- The **Docker** tool is the exception: it bind-mounts `/var/run/docker.sock` and
  the `$HOME`-based `~/.docker` instead of using a managed volume.

Each volume gets a private setup path so the host can initialize it as root after
the container starts (by default, `chown` to the host user). Synthetic Toby config is always
generated.

## Warnings

All warnings are suppressible via `settings.suppressWarnings`, which is always a
list: set it to `["*"]` to suppress everything, or to a list of specific IDs.

| ID | Meaning |
| --- | --- |
| `provider.model-discovery` | OpenCode provider model discovery failed. |
| `project.autoload-disabled` | `.toby.yaml` present but autoload is off. |
| `project.duplicate` | Duplicate configured project name skipped. |
| `project.missing` | Configured project path does not exist. |

## Launch config

Pass a per-run launch file with `--config <file>` (YAML, or JSON parsed by the
same YAML parser). A launch file describes one launch: its sandbox, projects,
tools, and working directory.

```yaml
name: foo                # optional; defaults to the first project name
container:
  image: mcr.microsoft.com/devcontainers/javascript-node:24-bookworm  # optional; defaults to mcr.microsoft.com/devcontainers/javascript-node:24-bookworm
  build:                    # optional; build an image before launch
    context: .              # defaults to this config file's directory
    dockerfile: Dockerfile.toby  # optional; relative to context, defaults to Dockerfile
  ports:                    # optional; publish sandbox ports to the host (Docker -p style)
    - "8080:3000"           # host 8080 → sandbox 3000
    - "127.0.0.1:9090:9090/udp"  # bind a specific host IP and protocol
settings:
  autoUpgrade: true      # optional; defaults to false
  mountProfile: work     # optional; namespaces this launch's persistent volumes
  suppressWarnings: ["*"] # optional; list of warning IDs, or ["*"] to suppress all
workdir: ~/tmp           # optional; defaults to the primary project path in the sandbox
project:
  foo:
    primary: true
  baz:                   # source defaults to $XDG_PROJECTS_DIR/baz
  bar:
    path: ../bar-source  # optional source; relative to this config file, leading ~ expands
tool:
  opencode:
    primary: true
    params: ["--model", "anthropic/claude-sonnet-4-5"]  # only valid on the primary tool
    mountProfile: work  # optional; namespaces this tool's persistent volumes
  uv:
  npm:
```

### `container.ports`

`container.ports` is a list of Docker-style publish specs that expose a sandbox
port on the host, the same syntax as `docker run -p`:
`[hostIP:][hostPort:]containerPort[/proto]` (e.g. `8080:3000`,
`127.0.0.1:9090:9090`, `5000/udp`). Each `--publish`/`-p` flag adds to the list.

- This is **launch-only** — it lives in the project `.toby.yaml` (or `--config`
  file) and the `--publish` flag, not in the host config.
- The bare `containerPort` form lets the daemon pick the host port.
- A published port reaches the host only if the in-sandbox service binds
  `0.0.0.0` (not just the container's loopback). Bracketed IPv6 host addresses
  are not supported.

### `project`

`project` is an object keyed by project name. A null value enables the project
with defaults. An object can set `path` and `primary`.

- The project appears inside the sandbox under the project root at
  `/toby/workspace/<name>`, regardless of where its host source lives.
- `path` is the host **source**. If omitted, it defaults to
  `$XDG_PROJECTS_DIR/<name>`. Explicit relative paths resolve from the config
  file directory; absolute paths are used as-is; leading `~` expands to the host
  home.
- Missing project paths are skipped with `project.missing`. Duplicate project
  names are skipped with `project.duplicate` (the same source may be mounted
  under different names).
- Toby Git and MCP repository names use the configured project **names**, not
  the host source paths.

### `tool`

Host config `tool` entries are defaults only and currently support
`mountProfile`. Launch config `tool` entries are enabled tools, keyed by tool
name. A null value enables the tool with defaults. An object can set
`mountProfile`, `primary`, and `params`. `params` is only honored when that tool
is the resolved primary tool: either it has `primary: true` in a config-owned
launch, it is the only configured tool, or it was selected on the CLI in an
overlay launch.

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
to the primary tool's configured `params`:

```sh
toby --config foo.yaml -- --additional-param value
```

With `exec` as the first tool you can run arbitrary commands. For example:

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
toby --config foo.yaml -- -- --watch
```

runs `npm test -- --watch` in `/toby/workspace/foo`.

## Autoload

Set `settings.autoloadProjectConfig: true` in **host** config to make direct
launches (e.g. `toby opencode my-app`) load `<project>/.toby.yaml` if present.
In autoload mode the CLI tool and project remain foreground and primary, and the
tools/projects from `.toby.yaml` are added (duplicate project names are skipped
after warning). If `.toby.yaml` exists but autoload is disabled, Toby emits
`project.autoload-disabled`.

The repository's own `.toby.yaml` is a minimal example:

```yaml
container:
  build:
    context: support/toby
tool:
  docker:
    primary: true
```
