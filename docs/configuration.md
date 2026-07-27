# Configuration

Toby has two strict configuration surfaces:

- host configuration controls sandbox image policy, MCP and models resources,
  permissions, instructions, and global settings; and
- launch configuration selects one run's projects, tools, workdir, settings,
  and optional sandbox overrides.

Unknown fields and invalid cross-field combinations are errors.

## Paths and environment

| Purpose | Environment variable | Default |
| --- | --- | --- |
| Host configuration | `XDG_CONFIG_HOME` | `~/.config` |
| Persistent Toby data | `XDG_DATA_HOME` | `~/.local/share` |
| Logs, caches, and transient overlays | `XDG_CACHE_HOME` | `~/.cache` |
| Runtime sockets | `XDG_RUNTIME_DIR` | required, no fallback |
| Projects | `XDG_PROJECTS_DIR` | `~/Projects` |

Every configured XDG path must be absolute. Sandbox and agent operations also
require `XDG_RUNTIME_DIR` to be clean. Toby does not change existing XDG parent
directories or configured symlinks; it follows them using ordinary kernel
access checks, and creates missing parents with the process umask. The resolved
Toby-owned `toby` directory is pinned before Toby operates beneath it and is
best-effort assigned to the effective user and group and restricted to mode
`0700`. A failed ownership or mode repair emits a debug diagnostic and
continues; the underlying filesystem operation reports an error only when the
requested access is not permitted.

Toby stores:

- verified OCI layouts, extracted bundle/rootfs objects, and reference mappings
  in `$XDG_DATA_HOME/toby/images`;
- home and tool volumes in `$XDG_DATA_HOME/toby/volumes`;
- OCI operation and MCP/models generation logs in
  `$XDG_CACHE_HOME/toby/logs`;
- run overlays in `$XDG_CACHE_HOME/toby/runs`; and
- the normal agent socket and run capabilities beneath
  `$XDG_RUNTIME_DIR/toby`.

A packaged system-wide socket can place the agent endpoint beneath
`/run/toby/users/<username>/toby`; it does not change the data, cache, or
launch-owned run-capability paths.

Static help and version output do not load host or launch configuration and do
not require `XDG_RUNTIME_DIR`. Image and volume management and the config-free
agent commands (`status`, `stop`, `resources`, and `logs`) also avoid
launch configuration. An invalid configuration file therefore cannot mask
`toby --help` or a subcommand's help. `agent models` and `agent cache flush`
do load host configuration because they resolve a configured models resource.

## Host configuration

Toby loads these files when present, in order:

1. `$XDG_CONFIG_HOME/toby/config.json`
2. `$XDG_CONFIG_HOME/toby/config.yaml`
3. `$XDG_CONFIG_HOME/toby/config.yml`

Mappings deep-merge. Later scalar and list values replace earlier values,
except `instructions` and `settings.suppressWarnings`, which are combined and
deduplicated in first-seen order.

The complete top-level shape is:

```yaml
instructions: []
sandbox: {}
permissions: {}
resources:
  mcps: {}
  models: {}
settings: {}
tools: {}
```

### `sandbox`

```yaml
sandbox:
  image: mcr.microsoft.com/devcontainers/javascript-node:24-bookworm
  pull: if-missing
```

`image` is the OCI registry reference used for application sandboxes. It is
optional and defaults to
`mcr.microsoft.com/devcontainers/javascript-node:24-bookworm`.

`pull` accepts:

- `if-missing` — use the cached published object when available, otherwise
  pull, verify, and extract it;
- `always` — resolve and pull the registry reference before selecting or
  preparing the immutable rootfs; or
- `never` — use only an already published per-user object.

The pull-policy default is `if-missing`.

### `instructions`

```yaml
instructions:
  - AGENTS.md
  - instructions/*.md
```

Entries are host paths or glob patterns. Relative paths resolve from the host
configuration directory, and a leading `~` expands to the host home. Toby reads
the matching files before launch and hands their contents to each selected tool
adapter.

Tool adapters write the combined instructions at their native private-home
paths as ordinary files that Toby replaces on each launch.

### `settings`

```yaml
settings:
  profile: default
  autoloadProjectConfig: false
  allowExternalProjects: false
  debug: false
  yolo: false
  managedTerminal: true
  suppressWarnings:
    - permission.auto-deny
```

| Key | Behavior |
| --- | --- |
| `profile` | Selects the home volume and default tool-volume profile for this launch. Defaults to `default`. |
| `autoloadProjectConfig` | Loads `<project>/.toby/config.yaml` during a direct launch. Defaults to false. |
| `allowExternalProjects` | Allows resolved direct-launch project paths outside `XDG_PROJECTS_DIR`. Relative paths still resolve from `XDG_PROJECTS_DIR`, so `../outside` selects its sibling only when this setting is enabled. Defaults to false. |
| `debug` | Enables raw, append-only startup output (including normally hidden lifecycle probes) and marks safe session introspection as debug-enabled. |
| `yolo` | Enables a selected tool's permission-bypass mode and approves actions governed by ordinary `ask` policy. |
| `managedTerminal` | Lets Toby mediate the foreground PTY and display approval prompts. Defaults to true. |
| `suppressWarnings` | Registered warning IDs, or `["*"]` for every warning. |

With `managedTerminal: false`, the foreground process uses plain terminal
passthrough. Actions that require a prompt and are not already allowed are
denied.

When `<project>/.toby/config.yaml` exists while
`autoloadProjectConfig` is false, Toby emits `project.autoload-disabled` and
continues without loading that file.

Registered warning IDs are:

- `agent.binary-version-mismatch`
- `project.autoload-disabled`
- `project.duplicate`
- `project.missing`
- `permission.auto-deny`

Human-readable diagnostics include the registered ID as
`warning[<id>]: <message>`. Structured diagnostics also include the same value
in the `warning_id` field so operators can filter warning records without
parsing the message.

### `tools`

Per-tool profile overrides select global tool volumes independently of the
launch-wide profile used by the home and other tools:

```yaml
settings:
  profile: work
tools:
  opencode:
    profile: personal
  codex:
```

Here the home, Codex, and other tools use `work`, while OpenCode uses
`personal`. A null tool entry has no override. Use the registered Toby tool
name as the key.

### `permissions`

```yaml
permissions:
  paths:
    /toby/home/shared: allow
  actions:
    git.commit: ask
    git.push: always-ask
```

`paths` are passed to supported application adapters. A leading `~` expands to
the host home while loading host configuration; use sandbox paths when granting
sandbox access. Toby supplies a default for `/tmp`; a configured rule for that
path overrides the default. `--yolo` adds `/`.

Action rules are `allow`, `deny`, `ask`, or `always-ask`. `always-ask` still
requires confirmation under `--yolo`. When a prompt cannot be displayed, an
action that requires one is denied.

### `resources.models`

```yaml
resources:
  models:
    local:
      protocol: openai
      name: Local models
      url: https://api.example.com/v1
      headers:
        Authorization: "Bearer {env:EXAMPLE_API_KEY}"
```

Models-resource `protocol` is `openai` or `anthropic`. `url` is required.
`name` is an optional display name. `headers` remain host-side and support
substitutions. Models are not declared in Toby configuration. During launch,
Toby asks the agent to query the configured API's models endpoint and writes
the discovered model map into generated tool configuration. Successful
discovery is cached by the agent for five minutes per effective models
resource.

Acquisition validates the HTTP or HTTPS base URL, bounded non-reserved headers,
and display metadata. This initial validation does not start Caddy or contact
the upstream; the launch's subsequent models-list request performs discovery.

Applications receive a loopback capability URL and a synthetic credential.
They never receive the configured URL or headers.

### Host substitutions

Models-resource headers and native MCP URLs, headers, command arguments, and
environment values support:

- `{env:NAME}` — the host environment variable value; and
- `{file:path}` — trimmed contents of a host file.

Relative file substitutions resolve from the host configuration directory.
Launches establish the agent session before resolving resource
substitutions. The detached agent starts with an allowlisted environment and
does not reload host configuration for resource definitions. Each resource
acquisition sends one resolved, input-only specification over the private
agent socket, so substitution variables do not need to enter the agent
process environment. Resource configuration is never returned by the agent.

## MCP configuration

All application-facing MCP definitions become stdio commands that invoke
`tobys resource connect -- <client-resource-id>`. The command uses the run-scoped
`toby.sandbox.v1` gRPC service. The generated client resource ID is a
per-launch UUID; configured names remain a host-side launch concern. The
configuration below describes the agent-side backend.

```yaml
resources:
  mcps: {}
```

Every key beneath `resources.mcps` names one MCP resource. The name `toby` is
reserved for the built-in server. A local HTTP resource may set its own
`idleTimeout`; otherwise it defaults to `10m`.

### Remote HTTP server

```yaml
resources:
  mcps:
    docs:
      type: remote
      transport: http
      url: https://example.com/mcp
      headers:
        Authorization: "Bearer {env:DOCS_TOKEN}"
```

A remote server must use `transport: http`. It accepts `url` and `headers` and
rejects all local-only fields. Each connector receives an independent
Streamable HTTP session.

### Local stdio server

```yaml
resources:
  mcps:
    search:
      type: local
      transport: stdio
      image: ghcr.io/example/search-mcp@sha256:...
      command: ["/usr/local/bin/search-mcp"]
      environment:
        SEARCH_TOKEN: "{env:SEARCH_TOKEN}"
      scope: user
      network: host
```

A local server requires:

- a nonempty OCI `image`;
- a command array whose first entry is the executable;
- `scope`: `user`, `home`, `project`, or `run`; and
- `network`: `host` or `private`.

`environment` is optional. `HOME` and `TOBY_SANDBOX` are runtime-controlled and
cannot be configured. A stdio server starts one Bubblewrap sidecar process per
connector and cannot define `endpoint` or `idleTimeout`.

### Local HTTP server

```yaml
resources:
  mcps:
    index:
      type: local
      transport: http
      image: ghcr.io/example/index-mcp@sha256:...
      command:
        - /usr/local/bin/index-mcp
        - --socket
        - /run/toby/index.sock
      endpoint:
        kind: unix
        socket: /run/toby/index.sock
        path: /mcp
      scope: home
      network: private
      idleTimeout: 2m
      mounts:
        - source: /srv/index-data
          target: /data
          access: read_only
          scope: home
```

The endpoint kind is `unix`. Its clean absolute socket must be
directly beneath `/run/toby`; `path` is the MCP HTTP request path.

Mount sources must be clean absolute host paths. Targets must be clean absolute
sandbox paths outside protected runtime locations. `access` is `regular` or
`read_only`, and mount `scope` is `home` or `project`. Each mount declares the
minimum scope of its data. Toby narrows the backend's effective sharing scope
when a mount is narrower than the requested server scope.

Matching local HTTP definitions may share one process generation. Connectors
still have independent Streamable HTTP session state. Idle teardown begins
after the final connector closes even if a launch remains registered; the next
connector restarts the resource through the same lease.

## Launch configuration

The strict launch schema is:

```yaml
name: review
sandbox:
  image: docker.io/library/node:24-bookworm-slim
  pull: if-missing
settings:
  profile: review
  autoUpgrade: false
  debug: false
  yolo: false
  suppressWarnings: []
projects:
  app:
    path: .
    primary: true
  library:
    path: ../library
tools:
  opencode:
    profile: personal
    primary: true
    params: ["--model", "local/example-model"]
  npm:
workdir: /toby/workspace/app
```

### Project configuration sets

The canonical project file is:

```text
<project>/.toby/config.yaml
```

When this exact path is loaded, Toby also reads regular files matching:

```text
<project>/.toby/config.d/*.yaml
```

Fragments are sorted lexically by filename and deep-merged after the base.
Nested mappings merge recursively; scalars and sequences are replaced by the
later source. Empty fragments are no-ops. Every individual source is checked
for unknown fields and invalid YAML before the merged document is decoded.

All relative paths from the base and fragments resolve from `<project>`, not
from `.toby` or `config.d`.

This convention supports a locally ignored override:

```yaml
# .toby/config.d/99-local.yaml
sandbox:
  pull: never
settings:
  debug: true
```

```gitignore
.toby/config.d/99-local.yaml
```

An arbitrary `--config` path loads only the selected YAML or JSON file. Toby
does not discover adjacent fragments, and relative paths resolve from that
file's directory.

The `.toby` directory, base file, and fragments must have safe regular
filesystem shapes. Toby rejects symlink replacement rather than following it
during configuration loading.

### `name`

`name` combines with `settings.profile` to identify the private home. In a
configuration-owned launch it defaults to the primary project name when
omitted. It does not identify a long-lived sandbox process.

### `sandbox`

The launch `sandbox.image` and `sandbox.pull` override host defaults. Direct
launch flags override both.

### `settings`

Launch settings are:

- `autoUpgrade`
- `profile`
- `debug`
- `yolo`
- `suppressWarnings`

The launch `profile` selects its home volume and the default tool volumes just
like the host setting.

`managedTerminal` is a host setting or CLI flag, not a launch-file key.
`--quiet` is also launch-only and has no configuration key. It suppresses
non-foreground startup output while preserving fatal errors and the
foreground application's original streams. Effective debug and quiet modes
are mutually exclusive.

### `projects`

Project entries are keyed by their sandbox name:

```yaml
projects:
  app:
  shared:
    path: ../shared
```

A null entry or omitted `path` uses `$XDG_PROJECTS_DIR/<name>`. Absolute paths
are used directly; a leading `~` expands to the host home. Unlike direct
`--project` selection, an explicit launch-file path may name a source outside
`XDG_PROJECTS_DIR`.

Projects appear at `/toby/workspace/<name>`. With multiple projects, exactly one
must set `primary: true`; with one project it is inferred. Missing additional
projects are skipped with `project.missing`. Duplicate names are skipped with
`project.duplicate`.

### `tools`

Entries use registered Toby tool names or CLI names:

```yaml
tools:
  opencode:
    profile: personal
    primary: true
    params: ["--model", "local/example-model"]
  npm:
```

With multiple tools, exactly one must be primary; with one it is inferred.
`params` is valid only for the resolved primary tool. `profile` overrides the
launch-wide `settings.profile` for that tool. Tool dependencies are added
automatically.

### `workdir`

`workdir` is a clean sandbox-visible path. A leading `~` expands to
`/toby/home`. When omitted, Toby uses the primary project's sandbox path.

### Config-owned and overlay launches

```sh
toby --config .toby/config.yaml
```

The configuration selects the private home through its name and profile, plus
the primary project and primary tool.

```sh
toby --config .toby/config.yaml codex app
```

The CLI tool and project remain primary; configured tools and projects are
additional. CLI arguments after `--` are appended to the primary tool's
configured `params`.

### Precedence

For values with equivalents at each layer:

```text
CLI flags > launch configuration > host configuration > built-in default
```

The selected launch configuration is resolved once and exposed as immutable
effective configuration to the rest of the run.
