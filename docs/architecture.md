# Architecture

This page describes how Toby is built and how the host and sandbox cooperate at
runtime. For day-to-day usage see the [README](../README.md); for the sandbox
and integration surface see [sandbox.md](sandbox.md); for configuration see
[configuration.md](configuration.md); for per-tool behavior see
[tools.md](tools.md); for diagnosing bring-up failures see
[debugging-sandbox-startup.md](debugging-sandbox-startup.md).

## Overview

Toby runs a development tool (OpenCode, Claude Code, Codex, Copilot, Grok, t3,
and others) inside a private-home Linux sandbox while keeping your real `$HOME`,
SSH keys, GPG setup, and credentials on the host. It is a single Go binary
(`petris.dev/toby`) that plays two roles:

- On the **host**, it launches the sandbox container, delivers the Toby binary,
  generates synthetic tool configuration, runs the tool and writes files into the
  container through the Docker API, and brokers privileged operations: host Git,
  and an HTTP reverse proxy to upstream MCP servers and model providers that
  injects credentials the sandbox never sees.
- Inside the **sandbox**, the same binary runs as `toby sandbox manager`: a
  proxy-only process that accepts the sandbox's outbound HTTP connections on a
  loopback listener and tunnels them to the host. The container's own main
  process is just `toby sandbox idle`, so the manager runs as a `docker exec`
  alongside it and `docker logs` stays empty.

The two halves talk over a single gRPC connection carried on the **manager
exec's stdio** (its stdin/stdout); the manager is the gRPC client and the host is
the server. Everything else the host does to the sandbox — running the tool, writing
context files, initializing mounts, tearing down — goes through the Docker API
(`docker exec`, `docker cp`, stop/remove), not an in-sandbox RPC service.

```
 host process (toby <tool> <env>)                 sandbox (container)
 ┌───────────────────────────────┐                ┌─────────────────────────────┐
 │ CLI / session orchestration   │   stdio (gRPC) │ toby sandbox manager         │
 │ Tunnel gRPC server  ◀─────────┼────────────────┤  gRPC client                 │
 │ HTTP reverse proxy (+creds)   │                │  local proxy listener        │
 │ MCP server (git.* tools)      │                │   127.77.0.1:47600           │
 │ docker exec / docker cp ──────┼───────────────▶│  launched tool (own tty)     │
 └───────────────────────────────┘                └─────────────────────────────┘
        host $HOME, SSH, GPG                          private $HOME + project bind
```

A tool inside the sandbox reaches an MCP server or model provider by making an
ordinary HTTP request to a proxied URL on the manager's loopback listener; the
manager tunnels the connection to the host over the gRPC link, and the host
attaches credentials and dials the real upstream. No host network port is opened
to the sandbox.

## Process model

Toby is wired together with [uber-go/fx](https://github.com/uber-go/fx)
dependency injection. `main.go` calls `app.Run()`, which builds a planning fx
application from `internal/app/module.go` and executes the Cobra CLI.

The root planning graph composes:

- `tools.PlanningModule()` — metadata-only tools used for command generation,
  config validation, dependency expansion, and launch-tool discovery.
- `sandbox.Module()` (`internal/control/sandbox`) — the proxy-only in-sandbox manager for
  the hidden `toby sandbox manager` command.
- Supporting providers: `config.NewPaths` (XDG path resolution),
  `appconfig.New`, `tool.NewRegistry`, and the session runner factory.

Each launch builds a separate execution fx graph. That graph contains only the
selected tool dependency closure from `tools.SelectedModule(...)`, the selected
sandbox runtime module when known, and the host-side services needed for that
run: `sandbox.Module()`, `host.Module()` (`internal/control/host`), `mcpserver.Module()`
(plus the `gitservice`/`sessionservice` service-plugin modules),
`contextfiles.NewService`, `executil.NewProcessRunner`, `tool.NewRegistry`, and
the lifecycle hook providers.

The CLI is built in `internal/cli`. `NewRootCommand` registers:

- one launch subcommand per registered tool that has launch help, via
  `Registry.LaunchTools()` (see `internal/cli/root.go`);
- the hidden `toby sandbox manager` command tree (`sandbox.go`);
- a shell-completion command.

The root `--config <file>` flag turns the invocation into a config-owned launch.

## Package layout

| Package | Responsibility |
| --- | --- |
| `internal/app` | fx application wiring and entry point. |
| `internal/cli` | Cobra commands, flag parsing, `toby sandbox` tree. |
| `internal/config/launch` | `--config` / `.toby.yaml` launch config parsing and resolution. |
| `internal/session/run` | End-to-end launch orchestration (`run.Run`). |
| `config` | XDG paths (`paths.go`). |
| `config/file` | Config file decoding (JSON/YAML), deep merge. |
| `internal/config/app` | Host config schema, validation, and context-file rendering (`appconfig`). |
| `context/files` | Context file session/builder for generated config and configured instructions. |
| `internal/context/setup` | Context lifecycle hooks for host config and tool context files. |
| `internal/control` | JSON-RPC 2.0 envelope (request/response/error types, codes, decoders), the capability `Router`, and the host-identity sentinels. Used in-process by the host Git capability. |
| `internal/control/tunnel` | gRPC-over-stdio transport: the `Tunnel` service (proto-generated), the host server that bridges tunneled connections into the reverse proxy, the client dialer/forwarder, and the fixed in-container proxy address. |
| `internal/control/stdio` | `net.Conn` over an independent reader/writer pair plus a one-shot `Listener`, so gRPC runs over the container's stdin/stdout. |
| `internal/control/methods/git` | The host-side Git capability: wire contract and handler `Service`, dispatched in-process. |
| `internal/control/host` | Host router for the in-process Git capability and the shared HTTP reverse proxy (`Service.HTTPProxy`). |
| `internal/control/sandbox` | The proxy-only in-sandbox manager: dials the host over stdio, binds the loopback listener, and forwards each accepted connection over the tunnel. |
| `internal/control/httpproxy` | Host reverse proxy that injects credentials and dials upstreams (`/proxy/<uuid>` path scheme), fed by the tunnel. |
| `internal/control/mcpserver` | Built-in Toby MCP server framework + per-session contract; service plugins live in `internal/control/mcpserver/services/{git,session}` (host Git tools; MCP lifecycle tools and `toby://` introspection resources). |
| `sandbox/runtime` | Host-side sandbox runtime: the Factory, the tool-facing sandbox service (docker exec/cp backed), and the Docker container backend (testcontainers-go for create, the moby client for cp/exec/attach). |
| `container/engine` | Shared Docker client and container service: tracks and tears down every container Toby starts. |
| `tools` | `Tool` contract, `Base`, `Metadata`, `Registry`, `Toolset` (clean). |
| `internal/lifecycle` | Launch phase runner driving the toolset through its phases (clean). |
| `sandbox` | Tool-facing sandbox interface (`Service`, `Paths`, `ExecOptions`) (clean). |
| `internal/tools/wiring` | Fx composition of the tool modules + planning metadata (clean). |
| `internal/tools/builtin/<name>` | One package per tool (claude, codex, t3, …) (clean). |
| `providers` (+ `openai`, `anthropic`) | Upstream `/models` discovery, fx-grouped provider clients behind a caching registry. |
| `diagnostic` | Exit-code mapping and suppressible warnings. |
| `platform/executil` | Process runner with signal forwarding. |
| `internal/version` | Build version string. |

## Control plane

There is a single host↔sandbox channel: a gRPC connection carried over the
manager exec's stdio. Everything host-initiated uses the Docker API instead.

### The stdio gRPC link (`internal/control/tunnel`, `internal/control/stdio`)

The run container's `Cmd` is `toby sandbox idle` — a process that only blocks
until teardown — so the container's own stdout stays empty and `docker logs` is
clean. The host then launches `toby sandbox manager` as a `docker exec` with **no
TTY**, so its stdout/stderr are Docker-multiplexed. Attaching to the exec returns
its stream from the first byte (so the manager's first gRPC bytes are not raced
past); the host demultiplexes the stream (`stdcopy`) into stdout (gRPC frames) and
stderr (manager logs) and wraps `(demuxed-stdout, exec-stdin)` as a `net.Conn`.
The manager wraps `(os.Stdin, os.Stdout)` the same way and routes its own logging
to stderr so fd 1 carries only gRPC frames.

The host runs the gRPC **server** over that single conn (via a one-shot
listener); the manager is the **client**. The link has no keepalive (a pipe
cannot honor deadlines). The `Tunnel` service has two methods:

- `Ready(addr)` — the manager calls it once, after binding its loopback proxy
  listener, to signal it is up; the host waits for this before proceeding.
- `Connect(stream Chunk)` — one bidirectional stream per proxied connection,
  carrying raw bytes both ways.

### Proxying (`internal/control/httpproxy`)

Remote MCP servers, local MCP sidecars, model providers, and the built-in Toby
MCP server are each registered as a reverse-proxy target keyed by a random UUID.
The proxied URL handed to the sandbox points at the manager's fixed loopback
listener — `http://127.77.0.1:47600/proxy/<uuid>` (a dedicated address in
127.0.0.0/8 so it never collides with anything a tool binds on 127.0.0.1). When
a tool connects, the manager accepts the connection and opens a `Connect` stream;
the host adapts that stream back into a `net.Conn`, feeds it to an in-process
`http.Server` whose handler is the reverse proxy, looks up the target, applies
the upstream URL and credential headers, and dials the real destination. Secrets
never enter the sandbox. The built-in Toby MCP target and local stdio MCP
sidecars dispatch to in-process handlers instead of an upstream URL; the built-in
server exposes host Git tools, MCP lifecycle tools, and `toby://docs/...` plus
`toby://session/...` text resources (session resources redact provider/MCP URLs,
headers, commands, argv, and environment values regardless of debug mode).

### Host-initiated operations (Docker API)

The host does not push files or commands over an RPC channel. Instead:

- **Run a command** — `docker exec` against the live container (`sandbox/runtime/exec.go`).
  The foreground primary tool always runs under a container PTY so it line-buffers
  and flushes its output; when the host is itself a terminal the exec stream is driven
  by `sandbox/runtime/foreground.go`, which puts the host terminal in raw mode and
  passes stdio straight through (so the tool talks to the real terminal directly and
  every capability query is answered by it). Alongside the passthrough it keeps a
  passive shadow terminal emulator (`charmbracelet/x/vt`) fed only by the tool's output
  — it never answers queries or sees input — so the host can overlay a popup (e.g. a
  permission prompt) over the live screen and then repaint the exact screen from the
  shadow when the popup is dismissed. When the host is not a terminal — a systemd
  service or a redirected run — the PTY stream is copied straight through to the host
  stdout so the output still reaches the journal. Install/configure/mount commands run
  non-interactively and return an exit code. The exec carries the resolved user
  (`uid:gid`, defaulting to the host user), working directory, and the host-held
  environment; supplementary groups come from the container's `GroupAdd` (set at create).
- **Write/replace files** — `docker cp` of an in-memory tar carrying mode + uid/gid
  (`sandbox/runtime/copy.go`); `*Owned` variants map directly to tar ownership.
  Deletes run as a root `docker exec rm`.
- **Deliver the binary** — `docker cp` into the created (not-yet-started) container.
- **Tear down** — stop and remove the container (or leave it under `--debug`).

The host-held environment lives in the sandbox service: it is seeded from the
container's base env (image defaults + request env) and mutated by tool
`SetEnvironment`/`Prepend`/`Append` calls, then injected into each subsequent
`docker exec`.

### Git (`internal/control/host`, `internal/control/methods/git`)

Host Git is dispatched **in-process** through `internal/control`'s JSON-RPC
envelope: the built-in Toby MCP server's Git tools encode a
request and call `host.Service.Handle`, which dispatches to the `git` capability.
Repository names and arguments are validated on the host, repositories must be
visible through the project bind, and `git` runs on the host so host config, SSH
agent, GPG signing, and credential helpers all apply. The host installs the
repository resolver on the git capability at session start.

Before running a sensitive operation, the capability asks the **approval service**
(`internal/approval`) for a decision, identified by the action's method name (e.g.
`git.push`) and the caller's default rule. The service resolves the decision with
`internal/permission`: an explicit `permissions.actions` rule wins, then
`settings.yolo`, then the explicit rule or the caller's default. When the outcome is
to ask, it prompts the user through the active interactive foreground — Toby's managed
terminal registers itself as the `sandbox.ApprovalPrompter`. The modal defaults to Deny,
and any session without that prompter — non-interactive, or with the managed terminal off
(`settings.managedTerminal: false`, which falls back to a plain passthrough) — denies
without prompting. A denied operation never runs.

### MCP sidecars

Configured `type: local` MCP entries are owned by the host session, not by the
tool running inside the main sandbox. During session startup Toby registers their
proxy URLs synchronously, starts the sidecar processes asynchronously, and does
not wait for MCP readiness before bringing up the main sandbox manager. Sidecars
run as their own containers; they do not use `toby sandbox manager`, do not run
setup hooks, and do not mount project, context, or managed-state paths. They use
the selected image defaults for user, home, and working directory. Under debug
mode sidecar containers are stopped but left on the host (not removed) for
inspection; restarts always create fresh container names and never reuse
previous containers. Each sidecar runtime
owns its own startup, HTTP preparation, cleanup, and introspection; runtime
details are returned as opaque `runtimeInfo` maps that generic code passes
through without interpreting.

## Sandbox runtime

Toby runs the sandbox in containers and requires a reachable Docker-compatible
daemon. Docker is the only backend; Podman and remote daemons work through the
standard `DOCKER_HOST` environment variable (e.g.
`DOCKER_HOST=unix:///run/user/1000/podman/podman.sock`), with no runtime
selection in Toby. The runtime uses testcontainers-go to create the container and
the moby client for `cp`/`exec`/`attach`/`wait`; image building still shells out
to `docker build`.

- **One container per session** (`sandbox/runtime`). It runs as `--user 0:0`
  with an init process and the configured image (default
  `mcr.microsoft.com/devcontainers/javascript-node:24-bookworm`), executing the
  proxy-only manager with its stdio as the gRPC link. `$HOME` (`/toby/home`) is
  backed by a named Docker volume (e.g. `toby.<profile>.runtime.home.default`) so
  private home state persists across runs; provider-backed managed mounts use lazy
  volumes named `toby.<profile>.<type>.<name>.<purpose>`. Each volume is mounted at
  **both** its final target and an isolated `/toby/mounts/<random>` setup path, so
  the host can chown it (root `docker exec`) without crossing into the binds
  layered under the final target. Projects bind-mount from the host under
  `/toby/workspace`. Empty volumes seed from the image on first mount. Under
  `settings.debug: true` or `--debug` the container is stopped on exit but left on
  the host (not removed) for inspection; containers are never reused.
- **Networking**: the container uses **bridge** networking. The manager's proxy
  listener is on the container's own loopback, so it stays container-private, while
  tools still have outbound network access. The gRPC control link rides the Docker
  attach stream (the API connection), not a network port, so it works the same for
  local, Docker Desktop, Podman, and remote daemons with nothing to tunnel. A launch
  may additionally publish sandbox ports to the host via Docker port bindings
  (`container.ports` / `--publish`), which set the container's `ExposedPorts` and
  `HostConfig.PortBindings` at create time; a published service must bind `0.0.0.0`
  inside the sandbox to be reachable from the host.

The runtime provides `sandboxpath.Paths`. The host-side `sandbox.SandboxService`
exposes those paths and centralizes sandbox file and command operations for tool
setup; it does not decide path policy. Runtime-specific introspection is provided
as an opaque `runtimeInfo` map.

The `container/engine` service owns the shared Docker client and tracks every
container Toby starts (the sandbox and MCP sidecars), terminating them
deterministically from an fx `OnStop` hook on session exit. Because Toby owns
teardown, testcontainers' Ryuk reaper is disabled (`TESTCONTAINERS_RYUK_DISABLED`).

Host secrets such as `~/.ssh` and `~/.gnupg` are not mounted into the sandbox.

### Helper binary delivery (`internal/sandbox/binary`)

The sandbox needs a Linux Toby binary to run as the manager. The host delivers it
with `docker cp` into the created container before starting it. On Linux the host
copies its own running binary from `/proc/self/exe`. macOS release builds embed a
matching Linux helper; local Darwin builds without the release embed tag require
`TOBY_LINUX_TOBY` to point at a Linux Toby binary.

## End-to-end launch flow

A direct launch such as `toby claude my-app` proceeds through the app session
runner and `internal/session/run/run.go`:

1. **Plan execution.** Parse CLI flags, merge host-config sandbox defaults, and
   (if enabled) autoload `<project>/.toby.yaml`. The planning registry expands the
   requested tools to include declared dependencies and determines the primary
   (foreground) tool. The app builds an execution fx graph scoped to that tool
   closure and the selected runtime.
2. **Register host-side mounts.** Build the execution toolset, apply managed mount
   settings, and run `toby.lifecycle.host.init` hooks, which register explicit
   binds and managed mount requests with the sandbox service before the sandbox
   starts.
3. **Register proxy targets.** Publish the built-in Toby MCP server and configure
   the MCP proxy. Provider and MCP upstreams are registered with the host reverse
   proxy; the sandbox is told their proxied URLs on the in-container listener.
4. **Start the sandbox.** Create the container (volumes multi-mounted at their
   final targets and setup paths), `docker cp` the Toby binary in, attach to its
   stdio, start it, serve the `Tunnel` gRPC over the stdio link, and wait for the
   manager's `Ready`.
5. **Initialize mounts.** Run `toby.lifecycle.sandbox.mount.init` hooks; the
   default chowns each provider volume to the host user via a root `docker exec` on
   its isolated setup path.
6. **Inject context.** Run `toby.lifecycle.sandbox.context.setup`, clear the
   generated context directory, then run `toby.lifecycle.sandbox.context.init`.
   Agent instructions and host config run before tool-specific hooks. Each hook
   writes files under the generated context directory via the sandbox service,
   which `docker cp`s them in.
7. **Install and launch.** Run `toby.lifecycle.sandbox.init`, then
   `toby.lifecycle.sandbox.install` or `toby.lifecycle.sandbox.upgrade` as needed
   (each command via `docker exec`). The primary tool's `Launch` runs the
   foreground command via `docker exec` under a container PTY, wired to the host
   terminal when there is one and otherwise streamed straight to the host stdout.
8. **Tear down.** When the foreground command exits, the host stops the gRPC
   server, stops and removes the container, and exits with the foreground
   command's exit code (under `--debug` the container is stopped but left on the
   host, not removed). A `SIGTERM` to the host process (e.g. `systemctl stop`)
   cancels the command's context, which unwinds the foreground command and runs
   this same teardown before the process exits, so the container is never left
   orphaned; the launch leaves `SIGINT` alone so an interactive Ctrl-C reaches the
   tool through its PTY instead.

## Tool abstraction (`tools` + `internal/lifecycle`)

Every full tool implements the `Tool` interface. Each tool package declares its
own identity (a `Name` constant and a `Meta` value) alongside its fx `Module`;
`internal/tools/wiring` enumerates those packages in one place. Tool implementations
register into the `tools` fx group in the execution graph and are looked up by
name in the `Registry`. Planning uses metadata-only `Tool` values built from the
same per-tool `Meta` (same names, CLI names, groups, and declared dependencies),
so no execution services are constructed.

A `Toolset` is an ordered collection with one primary tool. Dependencies select
additional tools; they do not directly run dependency code. The `Registry` orders
a launch's tools by a topological sort of their declared dependencies — every tool
runs after the tools it depends on (e.g. an agent runs after the npm it needs).
There is no priority number; ordering is derived from the dependency graph alone.

Lifecycle hook groups:

| Group | When | Typical use |
| --- | --- | --- |
| `toby.lifecycle.host.init` | before the sandbox starts | host-side setup, bind and managed-mount registration |
| `toby.lifecycle.sandbox.mount.init` | after the container starts, volumes mounted at their setup paths | root-owned mount setup, provider-volume ownership |
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

- **Exit codes** (`diagnostic/exitcode`): errors carry an exit code;
  `0` is success and unclassified errors map to `1`. Command execution maps
  `127` (not found), `126` (not executable), and `130` (canceled).
- **Warnings** (`diagnostic/warning`): suppressible IDs are
  `provider.model-discovery`, `project.autoload-disabled`,
  `project.duplicate`, and `project.missing`. Suppress all with
  `settings.suppressWarnings: ["*"]` or a subset with a list of specific IDs.
