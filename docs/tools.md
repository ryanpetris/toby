# Tools

Toby launches one primary foreground tool and may prepare other selected tools
in the same run. Each implementation owns its metadata, dependencies, native
storage requests, runtime assets, generated files, lifecycle phases, and launch
command.

Tool metadata separates the stable configuration name, CLI command name, and
human-readable display name. Lifecycle status rows use the display name as a
scope, and sandbox commands inherit it from their parent tool action.
Lifecycle commands provide a concise semantic `sandbox.ExecOptions.Status`
label that describes their intent rather than the executable, script, or
implementation detail being run. An omitted label uses the generic `Working`
fallback.

The registry topologically orders selected tools from their declared
dependencies. A launch never creates one sandbox process per tool: lifecycle
commands are direct Bubblewrap children that share the run plan, and the
primary tool is the final foreground child.

## Catalog

| Tool | CLI | Group | Installation |
| --- | --- | --- | --- |
| `opencode` | `opencode` | AI | `npm install -g opencode-ai` |
| `claude` | `claude` | AI | `npm install -g @anthropic-ai/claude-code` |
| `codex` | `codex` | AI | `npm install -g @openai/codex` |
| `copilot` | `copilot` | AI | `npm install -g @github/copilot` |
| `dcode` | `dcode` | AI | `uv tool install --prerelease allow deepagents-code` |
| `grok` | `grok` | AI | Linux archive from `x.ai/cli` |
| `speckit` | `speckit` | System | `uv tool install` from GitHub Spec Kit |
| `t3` | `t3` | AI | `npm install -g t3` |
| `emdash` | `emdash` | UI | Linux AppImage from GitHub releases |
| `docker` | `docker` | System | CLI must already exist in the OCI image |
| `npm` | `npm` | System | Supplied by the OCI image |
| `uv` | `uv` | System | Linux archive from GitHub releases |
| `github_cli` | `gh` | VCS | Linux archive from GitHub releases |
| `gitlab_cli` | `glab` | VCS | Linux archive from GitLab releases |
| `fj` | `fj` | VCS | Linux archive from Codeberg releases |
| `exec` | command arguments | Command | Not applicable |

## Installation

Most tools install into the private home on first use:

- npm tools use a prefix beneath `/toby/home/.local` and a private-home cache;
- uv tools use private-home tool and cache paths;
- archive installers are embedded scripts mounted transiently beneath
  `/run/toby`; and
- the base OCI image must supply installer prerequisites such as `curl`,
  `tar`, `git`, Node.js, or npm.

Use `--install` to install the primary tool and exit. Use `--upgrade` to force
its installer before launching.

Installer scripts, wrappers, and similar static assets are dedicated embedded
files mounted transiently beneath `/run/toby`.

## Native generated files

Coding-tool adapters receive a sandbox-safe session configuration containing:

- stdio MCP connector commands;
- models capability URLs and synthetic credentials;
- configured or discovered model metadata;
- exact sandbox-visible project roots;
- permission paths; and
- resolved instruction contents.

Each adapter renders complete ordinary files at the application's native paths.
Toby validates the combined file set, then atomically replaces every file
before lifecycle commands start.

These files are Toby-owned and replaced on later launches. Concurrent launches
sharing the same private home or tool volume use last-launch-wins behavior.

### OpenCode

Toby writes:

- `~/.config/opencode/opencode.json`
- `~/.config/opencode/toby/opencode.json`
- `~/.config/opencode/AGENTS.md`

Both configuration copies contain MCPs, providers, models, and permissions.
The dedicated Toby directory is configured as OpenCode's priority directory.

OpenCode requests separate global tool volumes for:

- `~/.config/opencode`
- `~/.local/share/opencode`

The selected profile chooses which volumes are mounted. Matching profiles
reuse the same native directories across projects and private homes, so
OpenCode's database and file locking operate on the normal host filesystem.

### Claude Code

Toby writes:

- `~/.config/claude/toby/mcp.json`
- `~/.config/claude/toby/settings.json`
- `~/.config/claude/CLAUDE.md`

Claude retains its ordinary writable config directory. Toby passes the
generated MCP and settings files through `--mcp-config` and `--settings`.
`--yolo` adds Claude's permission-bypass flag.

### Codex

Toby writes:

- `~/.codex/config.toml`
- `~/.codex/AGENTS.md`

The native config contains the MCP connector definitions and marks each
launch-selected sandbox project root as trusted. Toby writes the exact
`/toby/workspace/<project>` paths because Codex trust entries do not apply
recursively from `/toby`. Toby also passes the same MCP values as
highest-precedence `-c` launch overrides so project config cannot redirect the
run-scoped connections. `--yolo` adds Codex's explicit approval-and-sandbox
bypass flag.

### Copilot

Toby writes:

- `~/.copilot/mcp-config.json`
- `~/.copilot/copilot-instructions.md`

It launches Copilot with `--additional-mcp-config` pointing at the generated
file. `--yolo` adds `--allow-all-tools`.

### Deep Agents Code

Toby writes:

- `~/.deepagents/.mcp.json`
- `~/.deepagents/toby/AGENTS.md`

It launches with `--mcp-config` and defaults to the generated `toby` agent
unless the user selects another agent. When one matching Toby models resource
exists and the selected model names its protocol family, Toby supplies the
synthetic base URL and credential through Deep Agents' expected environment
variables.

### Grok

Toby writes:

- `~/.grok/managed_config.toml`
- `~/.grok/AGENTS.md`

The managed config is a regular file. Combined instructions are also passed
through Grok's `--rules` launch contract.

### T3 Code

T3 declares AI, UI, System, and VCS context groups. This gives its command a
`--with-<tool>` flag for matching tools:

```sh
toby t3 my-app --with-claude
toby t3 my-app --with-claude --with-codex --with-opencode
```

Selected coding tools are installed into the same private home and their native
configuration is generated before T3 launches.

The configuration equivalent is:

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

## System and VCS tools

`npm` and `uv` are usable directly and are dependencies of other tools. Their
writable global, tool, and cache locations stay in the private home.

`github_cli`, `gitlab_cli`, and `fj` run inside the sandbox. Operations that
need the host SSH agent, credential helpers, Git identity, or signing keys
should use the built-in Toby MCP Git tools instead.

`exec` runs an arbitrary command:

```sh
toby exec my-app -- npm test
```

It can also be the primary launch-config tool:

```yaml
projects:
  my-app:
tools:
  exec:
    primary: true
    params: ["npm", "test"]
  npm:
```

## Docker tool

The `docker` tool is Toby's only Docker integration. It:

- verifies that the `docker` CLI exists in the sandbox image;
- creates a per-run relay at `/run/toby/docker.sock`;
- clears an inherited `DOCKER_CONTEXT` and sets
  `DOCKER_HOST=unix:///run/toby/docker.sock`; and
- binds the host user's `~/.docker` at `/toby/home/.docker` read-only.

The Docker tool requests no Toby volume. Its client configuration and socket
relay both originate from the host only when the tool is selected.

The launch process connects each relay stream to `/var/run/docker.sock` with
its retained host credentials and supplementary groups. Bubblewrap receives
only the pinned relay endpoint: the host socket is not mounted, and the
sandbox still drops capabilities. Linux preserves supplementary kernel GIDs
across an unprivileged user namespace, but leaves them unmapped. Those
credentials can affect explicitly bound host paths, not an unmounted host
socket.

Selecting it is an explicit high-trust grant. Control of the host Docker daemon can
generally provide host-level control. No other built-in tool selects it as a
dependency, and normal launches do not probe for or expose Docker.
