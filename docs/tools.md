# Tools

Toby launches and manages a set of development tools inside the sandbox. Each
tool is a package under `internal/tools/builtin/` that registers into the tool registry
and implements the `Tool` lifecycle (see [architecture.md](architecture.md)).

You launch a tool with `toby <tool> <env>`. The tool you name becomes the
**primary** (foreground) tool. Additional tools can be installed alongside it,
either with `--with-<tool>` flags (for tools in the primary's context groups) or
through launch config `tool:` entries.

## Tool catalog

| Tool (`toby <name>`) | CLI binary | Group | Installed via | Synthetic config |
| --- | --- | --- | --- | --- |
| `opencode` | `opencode` | AI | `npm i -g opencode-ai` | MCP, providers, models, instructions |
| `claude` | `claude` | AI | `npm i -g @anthropic-ai/claude-code` | MCP, settings, instructions |
| `codex` | `codex` | AI | `npm i -g @openai/codex` | MCP, instructions |
| `copilot` | `copilot` | AI | `npm i -g @github/copilot` | MCP, instructions |
| `dcode` | `dcode` | AI | `uv tool install deepagents-code` | MCP, default Toby agent instructions |
| `grok` | `grok` | AI | download from `x.ai/cli` | MCP, rules |
| `speckit` | `specify` | AI | `uv tool install` from spec-kit | — |
| `t3` | `t3` | UI | `npm i -g t3` | inherits coding-tool config (see below) |
| `emdash` | `emdash` | UI | AppImage from GitHub releases | — |
| `docker` | `docker` | System | host (not installed) | — |
| `npm` | `npm` | System | host image / sandbox init | — |
| `uv` | `uv` | System | tarball from GitHub releases | — |
| `github_cli` | `gh` | VCS | tarball from GitHub releases | — |
| `gitlab_cli` | `glab` | VCS | tarball from GitLab releases | — |
| `fj` | `fj` | VCS | tarball from Codeberg releases | — |
| `exec` | (command) | Command | n/a | — |

All tools receive the built-in **Toby MCP server** (the `git.*` tools) when the
tool supports MCP. "Synthetic config" means Toby generates configuration under
the generated context directory (or injects it via launch flags) without touching the tool's
normal config files. See [configuration.md](configuration.md) for the host
config that feeds this generation, and [sandbox.md](sandbox.md) for the exact
generated files per tool.

## Installation model

Most tools install on demand into the sandbox home the first time they are
launched, so the private home persists their binaries across runs. Install
sources:

- **npm tools** (`opencode`, `claude`, `codex`, `copilot`, `t3`): `npm install
  -g <package>`. These depend on the `npm` tool, which sets `NPM_CONFIG_PREFIX`
  and `NPM_CONFIG_CACHE` to writable locations in the sandbox home.
- **Downloaded binaries** (`uv`, `gh`, `glab`, `fj`, `grok`, `emdash`): embedded
  shell installers fetch the latest release with `curl` and unpack it into the
  sandbox home. These need `curl` (and usually `tar`) in the sandbox image.
- **uv-based** (`dcode`, `speckit`): installed with `uv tool install`; `dcode`
  installs `deepagents-code`, while `speckit` installs from the GitHub `spec-kit`
  repository. These depend on the `uv` tool.
- **Host tools** (`docker`): not installed; the sandbox uses the host Docker
  socket and config via bind mounts.

Use `--install` to install the selected tool and exit, or `--upgrade` to
reinstall it before launching.

## Per-tool synthetic configuration

How each coding tool receives Toby's MCP server, providers, permissions, and
instructions. The full generated artifacts are documented in
[sandbox.md](sandbox.md); this is the summary.

Toby's built-in MCP server includes host Git tools, MCP lifecycle tools
(`mcp.start`, `mcp.stop`, `mcp.restart`) for Toby-managed local MCP sidecars,
and `toby://docs/...` plus `toby://session/...` resources. Session resources
never include provider or MCP headers, URLs, commands, argv, or environment
values. Host paths, Docker volumes, container names, and local MCP host ports
are included only when debug mode is enabled.

### OpenCode

Toby sets `OPENCODE_CONFIG_DIR` to the generated OpenCode context directory. The generated
`opencode.json` carries the Toby MCP server (as a remote `/proxy/<uuid>` URL),
configured remote/local MCP entries, providers translated to
`@ai-sdk/openai-compatible` / `@ai-sdk/anthropic`, and the combined
instructions. Provider models are used verbatim or discovered from `/models`.

### Claude Code

Toby sets `CLAUDE_CONFIG_DIR=$HOME/.config/claude` (so credentials and session
state persist in its managed Docker volume) and injects context
through launch flags rather than the config directory:

- `--mcp-config .../claude/mcp.json` — the Toby MCP server.
- `--append-system-prompt-file .../claude/instructions.md` — configured instructions.
- `--settings .../claude/settings.json` — generated settings.

### Codex

Codex has no session config-file flag, so Toby injects everything through
launch-time `-c` overrides: `mcp_servers.toby.url`, `mcp_servers.toby.enabled`,
configured MCP entries, and `developer_instructions`. It does not write to
`CODEX_HOME` or create a profile file.

### Copilot

Toby generates `copilot/mcp-config.json` and `copilot/AGENTS.md`, sets
`COPILOT_CUSTOM_INSTRUCTIONS_DIRS` to include the generated directory, and
launches with `--additional-mcp-config @.../copilot/mcp-config.json`.

### Deep Agents Code

Toby generates `dcode/mcp.json` and launches with
`--mcp-config .../dcode/mcp.json`, so Toby MCP and configured MCP servers are
loaded as a highest-precedence Deep Agents MCP overlay. By default Toby also
selects `--agent toby` and writes the generated instructions to
`~/.deepagents/toby/AGENTS.md`. If the user passes `--agent`, `--agent=...`,
or `-a`, Toby does not add `--agent toby` and does not modify the selected
agent's `AGENTS.md`. When the user explicitly launches with `--model openai:...`
or `--model anthropic:...` and exactly one matching Toby provider is configured,
Toby sets Deep Agents' provider API-key and base-URL environment variables to
point at the proxied provider without writing `~/.deepagents/config.toml`.

### Grok

Toby generates `grok/config.toml` and links `~/.grok/managed_config.toml` to it
during startup so Grok discovers Toby MCP through its native config loader.
Combined instructions are passed with `--rules`. Toby does not write
`~/.grok/config.toml`.

## t3 (T3 Code)

`t3` is a launcher UI that can drive the other coding tools. It is installed
with `npm install -g t3` and launched through a small wrapper script that
forwards signals to the `t3` child process.

Its key property is its **context groups**: t3 declares the AI, UI, System, and
VCS groups, so launching `toby t3` exposes a `--with-<tool>` flag for every tool
in those groups. Enabling a flag adds that tool to the sandbox (installed and
made available), and Toby generates that tool's synthetic configuration —
MCP server, providers, and instructions — so t3 can invoke it with Toby's
integration already wired up.

### Running t3 with one or more coding tools

Add the coding tools you want t3 to drive, then launch t3:

```sh
# T3 Code with Claude Code available inside the sandbox
toby t3 my-app --with-claude

# T3 Code with several coding tools available
toby t3 my-app --with-claude --with-codex --with-opencode
```

`my-app` is the environment/project name (mounted at `/toby/workspace/my-app` in the sandbox).
Each `--with-<tool>` flag installs that tool into the sandbox home and generates
its Toby integration config. Inside t3 you then select whichever tool you want;
its MCP (`git.*`), any configured providers, and your instruction files are
already in place.

You can pass extra arguments to t3 after `--`:

```sh
toby t3 my-app --with-claude -- <t3 arguments>
```

The equivalent in a launch config makes t3 the primary tool with the coding
tools listed after it:

```yaml
projects:
  my-app:
tools:
  t3:
    primary: true
  claude:
  codex:
```

```sh
toby --config t3.yaml
```

See [examples.md](examples.md) for more end-to-end recipes.

## System and VCS tools

- `npm` / `uv` — package managers, used both directly and as dependencies of
  other tools. They redirect their global/tool/cache directories into the
  sandbox home so installs persist and never touch the host.
- `docker` — binds the host Docker socket so containers run against the host
  daemon. It also explicitly bind-mounts `~/.docker` read-only; it does not use
  managed mounts (see [configuration.md](configuration.md#managed-mounts)).
- `github_cli` (`gh`), `gitlab_cli` (`glab`), `fj` — forge CLIs. For operations
  that need host credentials (signing, pushing over SSH), prefer the Toby Git
  MCP tools described in [sandbox.md](sandbox.md), which run on the host.
- `exec` — run an arbitrary command in the sandbox: `toby exec <env> -- <cmd>`,
  or as the first tool in a launch config to script the sandbox.
