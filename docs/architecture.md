# Architecture

This page describes how Toby is built and how the host and sandbox cooperate at
runtime. For day-to-day usage see the [README](../README.md); for the sandbox
and integration surface see [sandbox.md](sandbox.md); for configuration see
[configuration.md](configuration.md); for per-tool behavior see
[tools.md](tools.md).

## Overview

Toby runs a development tool (OpenCode, Claude Code, Codex, Copilot, Grok, t3,
and others) inside a private-home Linux sandbox while keeping your real `$HOME`,
SSH keys, GPG setup, and credentials on the host. It is a single Go binary
(`petris.dev/toby`) that plays two roles:

- On the **host**, it launches a sandbox runtime, runs a small control server,
  generates synthetic tool configuration, and brokers privileged operations
  (host Git, HTTP proxying to upstream MCP servers and model providers).
- Inside the **sandbox**, the same binary runs as `toby sandbox manager`,
  connects back to the host, applies the generated context, and executes the
  requested commands.

The two halves talk over a single authenticated WebSocket using JSON-RPC 2.0.
The wire protocol is documented as an OpenAPI schema in
[toby-control-openapi.yaml](toby-control-openapi.yaml).

```
 host process (toby <tool> <env>)                 sandbox (docker / bubblewrap)
 ┌───────────────────────────────┐                ┌─────────────────────────────┐
 │ CLI / session orchestration   │                │ /bin/sh bootstrap            │
 │ host manager (RPC handlers)   │   ws://.../control  curl /binary -> toby      │
 │ context init services         │◀──────────────▶│ toby sandbox manager         │
 │ MCP server (git.* tools)      │  JSON-RPC 2.0  │  file/command RPC handlers   │
 │ HTTP proxy  /proxy/<uuid>     │                │  runs the launched tool      │
 │ control server  127.0.0.1:0   │   GET /binary  │                              │
 └───────────────────────────────┘                └─────────────────────────────┘
        host $HOME, SSH, GPG                          private $HOME + project bind
```

## Process model

Toby is wired together with [uber-go/fx](https://github.com/uber-go/fx)
dependency injection. `main.go` calls `app.Run()`, which builds a planning fx
application from `internal/app/module.go` and executes the Cobra CLI.

The root planning graph composes:

- `tools.PlanningModule()` — metadata-only tools used for command generation,
  config validation, dependency expansion, and launch-tool discovery.
- `sandboxmanager.Module()` — sandbox-side control RPC handlers for the hidden
  `toby sandbox manager` command.
- Supporting providers: `config.NewPaths` (XDG path resolution),
  `tobyconfig.New`, `tool.NewRegistry`, and the session runner factory.

Each launch builds a separate execution fx graph. That graph contains only the
selected tool dependency closure from `tools.SelectedModule(...)`, the selected
sandbox runtime module when known, and the host-side services needed for that
run: `sandbox.Module()`, `hostmanager.Module()`, `mcpserver.Module()`,
`contextfiles.NewService`, `executil.NewProcessRunner`, `tool.NewRegistry`, and
the lifecycle hook providers.

The CLI is built in `internal/cli/commands`. `NewRootCommand` registers:

- one launch subcommand per registered tool that has launch help, via
  `Registry.LaunchTools()` (see `internal/cli/commands/root.go`);
- the hidden `toby sandbox manager` command tree (`sandbox.go`);
- a shell-completion command.

The root `--config <file>` flag turns the invocation into a config-owned launch.

## Package layout

| Package | Responsibility |
| --- | --- |
| `internal/app` | fx application wiring and entry point. |
| `internal/cli/commands` | Cobra commands, flag parsing, `toby sandbox` tree. |
| `internal/cli/launchconfig` | `--config` / `.toby.yaml` launch config parsing and resolution. |
| `internal/cli/session` | End-to-end session orchestration (`session.Run`). |
| `internal/config` | XDG paths (`paths.go`). |
| `internal/config/file` | Config file discovery, format parsing (JSON/JSONC/YAML), deep merge. |
| `internal/config/toby` | Host config schema, validation, and context-file rendering. |
| `internal/context/files` | Context file session/builder, embedded `GIT_AGENTS.md`. |
| `internal/context/setup` | Context lifecycle hooks for agent instructions and host config. |
| `internal/control` | Control transport: WebSocket, JSON-RPC peer, server, proxy, MCP. |
| `internal/control/hostmanager` | Host-side RPC handlers, host Git execution. |
| `internal/control/sandboxmanager` | Sandbox-side RPC handlers, command execution. |
| `internal/control/httpproxy` | `/proxy/<uuid>` reverse proxy for MCP and providers. |
| `internal/control/mcpserver` | Built-in Toby MCP server exposing host Git tools. |
| `internal/sandbox` | Runtime selection, shared sandbox service/types, helper binary delivery. |
| `internal/sandbox/docker` | Docker sandbox runtime implementation and Fx module. |
| `internal/sandbox/bubblewrap` | Bubblewrap sandbox runtime implementation and Fx module. |
| `internal/tools/tool` | Tool interface, lifecycle, registry, toolset, state settings. |
| `internal/tools/<name>` | One package per tool (claude, codex, t3, …). |
| `internal/tools/toolconfig` | Helpers for generating synthetic tool config. |
| `internal/providers/openai` | Upstream `/models` discovery for OpenAI-compatible providers. |
| `internal/diagnostic` | Exit-code mapping and suppressible warnings. |
| `internal/platform/executil` | Process runner with signal forwarding. |
| `internal/version` | Build version string. |

## Control protocol

The control channel is JSON-RPC 2.0 over a single persistent WebSocket
(`internal/control/websocket.go`, `peer.go`). The host listens on `127.0.0.1`
with an ephemeral port and a per-run bearer token; the sandbox connects to
`ws://$TOBY_CONTROL_HOST/control` with that token. Both sides can issue
requests over the same connection (`internal/control/peer.go`).

The same host listener also serves:

- `GET /binary` — the sandbox helper binary download (bearer-token protected).
- `/proxy/<uuid>` — per-run HTTP reverse-proxy targets for remote MCP servers,
  model providers, and the built-in Toby MCP server.

### Methods

| Method | Direction | Purpose |
| --- | --- | --- |
| `context.init` | sandbox → host | First message; triggers context injection. |
| `file.create` | host → sandbox | Write a file (`path`, `data`, `mode`, `uid`, `gid`). |
| `file.mkdir` | host → sandbox | Create a directory (`path`, `mode`, `uid`, `gid`). |
| `file.delete` | host → sandbox | Remove a file or directory (`recursive`). |
| `file.symlink` | host → sandbox | Create a symlink (`path`, `target`). |
| `command.run` | host → sandbox | Run a command (`command_id`, `argv`, `foreground`, `hide_output`). |
| `command.exit` | sandbox → host | Report a finished command (`command_id`, `exit_code`). |
| `sandbox.terminate` | host → sandbox | Ask the sandbox manager to shut down. |
| `git.commit` / `git.fetch` / `git.push` / `git.rebase` / `git.tag` | sandbox → host | Host Git operations for visible repositories. |

JSON-RPC error codes follow the standard set (`-32700` parse, `-32600` invalid
request, `-32601` method not found, `-32602` invalid params, `-32603` internal)
plus a Toby-specific `-32007` for "project not visible".

### Host side (`internal/control/hostmanager`)

The host manager accepts a sandbox connection, requires the first message to be
`context.init`, then runs the registered context lifecycle hooks to populate the
sandbox before handing control to the launched tool. It services
`command.exit` notifications (to track foreground completion) and the `git.*`
methods. Git execution lives in `git.go`/`git_service.go`: repository names are
validated (relative to the projects root, no empty/`.`/`..` segments, must be
visible through the project bind), arguments are checked, and `git` runs on the
host so host config, SSH agent, GPG signing, and credential helpers all apply.

### Sandbox side (`internal/control/sandboxmanager`)

`toby sandbox manager` dials the host, sends `context.init`, and then serves the
`file.*`, `command.run`, and `sandbox.terminate` methods. `command.run` spawns a
child process tracked by `command_id`; at most one command may be `foreground`.
The manager applies the requested uid, gid, and supplementary groups to child
commands when it has permission; host-driven command execution defaults to the
host uid/gid/groups. It also removes `TOBY_CONTROL_HOST` and
`TOBY_CONTROL_TOKEN` from child command environments. The manager forwards
SIGINT/SIGTERM/SIGHUP/SIGQUIT to the foreground process and reports completion
with `command.exit`. On `sandbox.terminate` it shuts down gracefully (SIGTERM
then SIGKILL after a short grace period).

### HTTP proxy (`internal/control/httpproxy`)

Remote MCP servers, model providers, and the built-in Toby MCP server are each
registered as a proxy target keyed by a random UUID and exposed at
`http://<control-host>/proxy/<uuid>`. The host applies upstream URLs and
credential headers when forwarding, so secrets never enter the sandbox. The
built-in Toby MCP target dispatches to the in-process MCP handler instead of an
upstream URL.

## Sandbox runtimes

Toby selects the available runtime with the lowest priority number: Docker has
priority 0 and Bubblewrap priority 1, so Docker is preferred when present. The
runtime can be forced with `--sandbox-runtime` or `sandbox.runtime.default`.

- **Docker** (`internal/sandbox/docker`): `docker run --rm --init --user 0:0` with the configured image
  (default `mcr.microsoft.com/devcontainers/javascript-node:24-bookworm`). `$HOME` (`/toby/home` by default) is backed by
  a named Docker volume (e.g. `toby.<profile>.runtime.home.default`) so private
  home state persists across runs; provider-backed managed mounts use lazy
  volumes named `toby.<profile>.<type>.<name>.<purpose>`. Projects bind-mount from the host
  under `/toby/workspace`. The image can be built from
  `sandbox.runtime.docker.build`.
- **Bubblewrap** (`internal/sandbox/bubblewrap`): a `bwrap` sandbox that bind mounts
  provider-backed directories from `sandbox.runtime.bubblewrap.root` by provider
  ID, keeps projects under `$XDG_PROJECTS_DIR`, and stores Toby internals under
  `${XDG_RUNTIME_DIR:-/run/user/<uid>}/toby`.

The selected runtime provides `sandboxpath.Paths`. The host-side
`sandbox.SandboxService` exposes those paths and centralizes sandbox file and
command operations for tool setup; it does not decide Docker or Bubblewrap path
policy.

In both runtimes, host secrets such as `~/.ssh` and `~/.gnupg` are not mounted.

### Helper binary delivery (`internal/sandbox/binary`)

The sandbox needs a Linux Toby binary to run as the manager. On Linux the host
serves its own running binary from `/proc/self/exe`. macOS release builds embed
a matching Linux helper; local Darwin builds without the release embed tag
require `TOBY_LINUX_TOBY` to point at a Linux Toby binary.

## End-to-end launch flow

A direct launch such as `toby claude my-app` proceeds through the app session
runner and `internal/cli/session/session.go`:

1. **Plan execution.** Parse CLI flags, merge host-config sandbox defaults, and
   (if enabled) autoload `<project>/.toby.yaml`. The planning registry expands
   the requested tools to include declared dependencies and determines the
   primary (foreground) tool. The app then builds an execution fx graph scoped to
   that tool closure and the selected runtime.
2. **Register host-side mounts.** Build the execution toolset, apply managed
   mount settings, and run `toby.lifecycle.host.init` hooks. These hooks register
   explicit binds and managed mount requests with the sandbox service before the
   sandbox starts.
3. **Start the control server.** Listen on `127.0.0.1:0`, mint a random
   `TOBY_CONTROL_TOKEN`, register the binary source and HTTP-proxy routes, and
   set calculated `HOME` plus `TOBY_CONTROL_HOST`/`TOBY_CONTROL_TOKEN` in the
   sandbox manager startup environment.
4. **Prepare mounts.** Docker primes volumes in their final locations, then runs
   a setup sandbox with home and provider volumes mounted at isolated
   `/toby/mounts/<random>` paths so runtime and `toby.lifecycle.sandbox.mount.init`
   hooks can run setup commands as root without crossing into nested mounts.
5. **Launch the sandbox.** The runtime runs a `/bin/sh` bootstrap that creates
   the runtime's Toby bin directory, downloads the helper from `/binary` with
   the bearer token, marks it executable, and `exec`s `toby sandbox manager` by
   absolute path.
6. **Bootstrap the manager.** Inside the sandbox the manager connects back over
   the control WebSocket and sends `context.init`.
7. **Inject context.** The host runs `toby.lifecycle.sandbox.context.setup`,
   clears the generated context directory, then runs
   `toby.lifecycle.sandbox.context.init`. Agent instructions and host config run
   before tool-specific context hooks. Each hook writes files under the generated
   context directory via the sandbox service and sandbox manager `file.create`
   calls.
8. **Install and launch.** The host runs `toby.lifecycle.sandbox.init`, then
   `toby.lifecycle.sandbox.install` or `toby.lifecycle.sandbox.upgrade` as
   needed. The primary tool's `Launch` runs the foreground command via
   `command.run`.
9. **Tear down.** When the foreground command exits, the host sends
   `sandbox.terminate`; the host process exits with the foreground command's
   exit code.

## Tool abstraction (`internal/tools/tool`)

Every full tool implements the `Tool` interface. Tool implementations register
into the `toby.tools` fx group in the execution graph and are looked up by name
in the `Registry`. Planning uses metadata-only `Tool` values with the same names,
CLI names, groups, declared dependencies, and lifecycle priorities.

A `Toolset` is an ordered collection with one primary tool. Dependencies select
additional tools; they do not directly run dependency code. Static lifecycle
priority controls hook ordering, so dependency tools use lower priorities than
their dependents.

Lifecycle hook groups:

| Group | When | Typical use |
| --- | --- | --- |
| `toby.lifecycle.host.init` | before the sandbox starts | host-side setup, bind and managed-mount registration |
| `toby.lifecycle.sandbox.mount.init` | setup sandbox after managed mounts are attached | root-owned mount setup, provider-volume ownership |
| `toby.lifecycle.sandbox.context.setup` | before context files are generated | set environment variables, append PATH entries |
| `toby.lifecycle.sandbox.context.init` | context injection | emit synthetic config / instructions |
| `toby.lifecycle.sandbox.init` | once per sandbox | run embedded install/init scripts |
| `toby.lifecycle.sandbox.install` | before launch or `--install` | install missing tools |
| `toby.lifecycle.sandbox.upgrade` | `--upgrade` / auto-upgrade | reinstall tools |

Only the primary tool's `Launch` method runs in the foreground after lifecycle
hooks complete.

Tools are grouped (`GroupAI`, `GroupUI`, `GroupSystem`, `GroupVCS`,
`GroupCommand`). A tool declares `ContextGroups()`; launching it exposes a
`--with-<tool>` flag for each tool in those groups, so a launcher like **t3**
can pull in and drive the coding tools. See [tools.md](tools.md) for the full
matrix and the t3 walkthrough.

## Diagnostics

- **Exit codes** (`internal/diagnostic/exitcode`): errors carry an exit code;
  `0` is success and unclassified errors map to `1`. Command execution maps
  `127` (not found), `126` (not executable), and `130` (canceled).
- **Warnings** (`internal/diagnostic/warning`): suppressible IDs are
  `mount.host-backing`, `opencode.model-discovery`, `project.autoload-disabled`,
  `project.duplicate`, and `project.missing`. Suppress all with
  `settings.suppressWarnings: true` or a subset with a list of IDs.
