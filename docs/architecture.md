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

- On the **host**, it runs as a long-lived **daemon** plus thin **clients**. The
  daemon owns Docker and manages three kinds of container (see below): a shared
  **home** container per profile, a **netns** container per project+profile, and an
  ephemeral **tool** container per invocation. It delivers the Toby binary (in a
  read-only per-version volume), generates synthetic tool configuration, asks the
  home manager to write files and run installs, and brokers privileged operations:
  host Git, and an HTTP reverse proxy to upstream MCP servers and model providers
  that injects credentials the sandbox never sees. A client is any other `toby`
  invocation; it asks the daemon to bring a project up and then attaches to the
  tool container the daemon creates.
- Inside a sandbox, the same binary runs one of three `toby sandbox <role>`
  managers. The **home** manager (`sandbox home`, root) owns the shared `/toby/home`
  volume and serves file operations and streamed installs/exec. The **netns**
  manager (`sandbox netns`, root) owns the published ports and the network
  namespace, and accepts the sandbox's outbound HTTP connections on a loopback
  listener and tunnels them to the host. The **tool** container's main process is
  `toby sandbox launch <descriptor>`, which sets the environment, `chdir`s, and
  `syscall.Exec`s the actual tool as PID 1 — the client attaches to it directly. The
  home and netns containers' own main process is `toby sandbox idle`, so their
  managers run as a `docker exec` alongside and `docker logs` stays empty.

The home and netns managers each talk to the host over a single gRPC connection
carried on the **manager exec's stdio**; the manager is the gRPC client and the host
is the server. Installs and file operations are JSON-RPC requests over the home
manager's control stream (install output streams back to the client); the tool
container joins the netns via Docker's `NetworkMode=container:<netns>`. The Toby
binary is delivered once per version in a read-only `toby.bin.<key>` volume mounted
into every container, so there is no per-container `docker cp`.

```
 host daemon                                      containers
 ┌───────────────────────────────┐                ┌─────────────────────────────┐
 │ session orchestration         │   stdio (gRPC) │ home  (root, per profile)    │
 │ home registry (files+exec) ◀──┼────────────────┤  sandbox home: files + exec  │
 │ netns registry (proxy/mcp)  ◀─┼────────────────┤ netns (root, per proj+prof)  │
 │ HTTP reverse proxy (+creds)   │                │  sandbox netns: proxy 127.77 │
 │ MCP server (git.* tools)      │   attach (pty) │ tool  (uid:gid, ephemeral)   │
 │ tool container create ────────┼───────────────▶│  sandbox launch → the tool   │
 └───────────────────────────────┘                └─────────────────────────────┘
        host $HOME, SSH, GPG            shared /toby/home + netns + project bind
```

A tool inside the sandbox reaches an MCP server or model provider by making an
ordinary HTTP request to a proxied URL on the manager's loopback listener; the
manager tunnels the connection to the host over the gRPC link, and the host
attaches credentials and dials the real upstream. No host network port is opened
to the sandbox.

## Daemon and clients

The first `toby` invocation auto-spawns a background **daemon** (`toby daemon`),
and every later invocation is a thin **client** that connects to it. The daemon
binds a control endpoint under `XDG_RUNTIME_DIR` (by default a unix socket at
`$XDG_RUNTIME_DIR/toby/daemon.sock`), owns Docker, and keeps the **shared home**
container (per profile) and the **netns** container (per project+profile) warm and
reused across invocations. A launch (`toby <tool> <env>`) becomes a client that asks
the daemon to acquire the netns unit + the profile's shared home, run the tool
lifecycle (install/configure/context) against the home manager, write a launch
descriptor into the home volume, and create a **tool** container joined to the
netns; the daemon returns that container's id and the **client attaches to it and
starts it**, so the interactive PTY, raw mode, resize, and the approval modal attach
to the user's real terminal — the daemon never allocates a PTY. When the tool exits
the client releases its session: the daemon terminates the ephemeral tool container
and drops both holds. The home and netns containers are kept warm and torn down
after an idle timeout (default 15m), and the daemon auto-shuts-down when it has no
live projects unless it was started with `--no-idle-shutdown` (supervised/systemd
mode).

The daemon's control surface is a small set of JSON-RPC methods carried over a
transport abstraction (see [Control plane](#control-plane) below): `daemon.ping`
(ensure-up + version), `daemon.status` (list projects), `daemon.stop` (shut down,
tearing down every project container), `session.start` (bring the project up and
return the exec plan), and `session.release` (drop a session's hold). Approval
prompts round-trip the other way — the daemon calls back to the owning client with
`approval.prompt`, which drives that client's foreground modal.

The daemon watches the config file (mtime/size polling in
`internal/daemon/configwatch`, no filesystem-notify dependency) and reloads it on
change; **new** project launches pick up the change, while an **already-running**
project keeps the config it launched with — its configuration is frozen at the
launch that created its container.

Local **MCP sidecar containers are shared across projects**: the daemon-root
`internal/daemon/resource` registry runs one container per configured local MCP
server (keyed by its resolved config, so identical config shares one container and a
changed config yields a new one), refcounted; each project's `mcpproxy` layer
acquires a lease and registers the shared backend on its own reverse proxy under a
fresh URL. The container stops when the last project holding a lease exits. Remote
MCP servers and providers are direct upstreams with no container.

## Process model

Toby is wired together with [uber-go/fx](https://github.com/uber-go/fx)
dependency injection. `main.go` calls `app.Run()`. `toby daemon [ping|status|stop]`
is dispatched first (`internal/app/daemon.go`) into its own lighter fx graph; every
other invocation builds the launch graph from `internal/app/module.go` and executes
the Cobra CLI, whose session runner is the daemon **client**
(`internal/app/client_runner.go`).

The **daemon** graph composes `wiring.PlanningModule()`, `tools.Module()`, the
config watcher, a transport module, and `daemon.Module()` — which collects the
`daemon.*`/`session.*` control capabilities into a `control.Router`, builds the
`daemon.Service` accept loop over the transport listener, and stands up the
project registry. Each project the daemon serves gets its own **per-project fx
graph** built from `internal/session/graph` (the host services, the selected tool
closure, the sandbox runtime, the lifecycle runner, the providers, and the
session-config resolver); `run.BringUp` (`internal/session/run`) then stands the
container up and holds it open across launches (`BringUp`/`Install`/`LaunchPlan`/
`Close`).

The **launch/client** graph composes `sandbox.Module()`, the planning tools, a
transport module, and `client.Module()`; its Cobra session runner marshals the
resolved options into a `session.start` request, runs the returned plan against
Docker, and releases the session on exit.

The root planning graph composes:

- `tools.PlanningModule()` — metadata-only tools used for command generation,
  config validation, dependency expansion, and launch-tool discovery.
- `sandbox.Module()` (`internal/control/sandbox`) — the in-sandbox manager for
  the hidden `toby sandbox manager` command.
- Supporting providers: `config.NewPaths` (XDG path resolution),
  `appconfig.New`, `tool.NewRegistry`, the transport, and the client-backed
  session runner factory.

Each project builds a separate per-project fx graph (`internal/session/graph`).
That graph contains only the selected tool dependency closure from
`wiring.SelectedModule(...)`, the sandbox runtime module, and the host-side services
needed for that project: `host.Module()` (`internal/control/host`),
`mcpserver.Module()` (plus the `gitservice`/`sessionservice` service-plugin modules),
`mcpproxy.Module()`, `contextfiles.NewService`, `executil.NewProcessRunner`,
`tool.NewRegistry`, and the lifecycle hook providers.

The CLI is built in `internal/cli`. `NewRootCommand` registers:

- one launch subcommand per registered tool that has launch help, via
  `Registry.LaunchTools()` (see `internal/cli/root.go`);
- a shell-completion command.

The hidden `toby sandbox <role>` commands (`home`/`netns`/`launch`/`idle`) are not
Cobra subcommands — they are dispatched early in `app.Run` (`internal/app/sandbox.go`),
before the launch CLI is built, since they run inside containers and need no fx graph.

The root `--config <file>` flag turns the invocation into a config-owned launch.

## Package layout

| Package | Responsibility |
| --- | --- |
| `internal/app` | fx application wiring, entry point, daemon/client dispatch (`daemon.go`), and the client-backed session runner (`client_runner.go`). |
| `internal/cli` | Cobra commands, flag parsing, `toby sandbox` tree. |
| `internal/config/launch` | `--config` / `.toby.yaml` launch config parsing and resolution. |
| `internal/daemon` | The long-lived daemon: `Service` accept loop, the `daemon.*`/`session.*` control capabilities, and the per-project+profile netns bring-up `Lifecycle`. |
| `internal/daemon/project` | Race-safe registry and container state machine for the per-project+profile **netns** unit (bring-up/idle-teardown/stop). |
| `internal/daemon/home` | Daemon-root registry of the shared per-profile **home** containers (one home per profile, shared across projects, with a per-home install mutex). |
| `internal/daemon/protocol` | Dependency-free client↔daemon wire contract (JSON-RPC method names + param/result DTOs). |
| `internal/daemon/transport` (+ `transport/unixsocket`, `transport/websocket`) | Swappable client↔daemon transport seam and its two implementations. |
| `internal/daemon/configwatch` | Polls the config files' mtime/size and reloads the daemon's config, holding the last-good config through a bad edit. |
| `internal/client` | Thin client: dials/spawns the daemon, issues control requests, **attaches to** the ephemeral tool container, streams install output, and hosts the approval prompter. |
| `internal/session/graph` | The shared per-project+profile fx graph (host services, tool closure, runtime, lifecycle), consumed by the daemon. |
| `internal/session/run` | The netns unit held open across launches (`BringUp` + `PreLaunch`/`Install`/`Close`): stands up the netns container + proxy/MCP and, per session, runs the tool lifecycle against the shared home and creates the tool container. |
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
| `internal/control/sandbox` | The three in-sandbox manager roles: `home` (files + streamed exec over the control stream), `netns` (binds the loopback listener and forwards each accepted connection over the tunnel), and `launch` (reads the descriptor and `syscall.Exec`s the tool). |
| `internal/control/methods/exec` | The sandbox-side streamed exec capability (`exec.run` → run as uid:gid, `exec.output` notifications) used by the home manager for installs. |
| `internal/control/httpproxy` | Host reverse proxy that injects credentials and dials upstreams (`/proxy/<uuid>` path scheme), fed by the tunnel. |
| `internal/control/mcpserver` | Built-in Toby MCP server framework + per-session contract; service plugins live in `internal/control/mcpserver/services/{git,session}` (host Git tools; MCP lifecycle tools and `toby://` introspection resources). |
| `sandbox/runtime` | Host-side sandbox runtime: the Factory (options→Spec), the tool-facing sandbox service (home-manager file/exec ops + netns-proxied URLs), the three container builders (home/netns manager stand-up + tool container create), the read-only bin volume, and the client attach driver. |
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

There are two distinct channels. The **client↔daemon** channel carries the
JSON-RPC control methods between a `toby` client and the daemon. The
**host↔sandbox** channel is a single gRPC connection carried over the manager
exec's stdio, owned by the daemon per project; everything the daemon initiates
against a sandbox otherwise uses the Docker API.

### The client↔daemon transport (`internal/daemon/transport`)

The client↔daemon channel is a JSON-RPC peer connection (the same `control.Peer`
framing used in-process) carried over a swappable **transport**. The `transport`
package defines only the interfaces both ends depend on — `Listener` (daemon
accepts connections), `Connector` (client dials), and `Bootstrap` (client's
detect-or-spawn step) — so a concrete transport only has to yield `net.Conn`s and
own its endpoint. Two implementations exist, selected by the `TOBY_TRANSPORT`
environment variable and wired at the composition root (`internal/app/daemon.go`):

- **unix socket** (default, `transport/unixsocket`) — binds
  `$XDG_RUNTIME_DIR/toby/daemon.sock` (0700 dir). `EnsureDaemon` does a
  flock-guarded detect / stale-socket-cleanup / detached `toby daemon` spawn /
  poll dance so exactly one daemon binds even under concurrent first invocations.
- **WebSocket** (`transport/websocket`, `golang.org/x/net/websocket`) — a single
  `/rpc` upgrade endpoint on a loopback address (default `127.0.0.1:47700`,
  overridable with `TOBY_WS_ADDRESS`).

Because `control.Peer` carries the framing, both transports carry the exact same
JSON-RPC payloads; only the byte carrier differs. The channel is bidirectional:
client-initiated methods (`daemon.*`, `session.*`) drive a session, and
daemon-initiated methods (`approval.prompt`, `status.progress`) call back to the
client that owns a session.

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

The host uses both Docker and the manager control stream:

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
- **Write/replace files** — JSON-RPC `file.*` requests over the manager control stream.
  The manager creates, deletes, mkdirs, and symlinks paths inside the sandbox with
  container filesystem semantics.
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

- **Three container kinds** (`sandbox/runtime`), all mounting the read-only
  `toby.bin.<key>` volume at `/toby/bin` and the configured image (default
  `mcr.microsoft.com/devcontainers/javascript-node:24-bookworm`):
  - **home** (per profile, `toby.home.<profile>`, `--user 0:0`, init): owns the
    shared `$HOME` (`/toby/home`) volume `toby.<profile>.runtime.home`, so
    installed tools and tool state persist and are shared across every project on the
    profile. Runs the `sandbox home` manager (files + streamed exec) over its exec
    stdio; the daemon chowns `/toby/home` to the invoking user once (marker-guarded).
  - **netns** (per project+profile, `toby.net.<digest>.<profile>`, `--user 0:0`,
    init): owns the published ports and the network namespace, and runs the
    `sandbox netns` proxy manager. Brought up once and reused across launches.
  - **tool** (per invocation, `toby.tool.<sid>`, `--user uid:gid`, ephemeral): joins
    the netns via `NetworkMode=container:<netns>`, mounts the home volume + the
    workspace project binds, and runs `sandbox launch <descriptor>` as its main
    process. The client attaches to it; the daemon terminates it on release.

  Under `settings.debug: true` or `--debug` the home/netns containers are stopped on
  exit but left on the host (not removed) for inspection; containers are never reused.
- **Networking**: the netns container uses **bridge** networking and owns the shared
  namespace the tool container joins. The netns manager's proxy listener is on the
  namespace's loopback (`127.77.0.1:47600`), so it stays private while tools still
  have outbound network access. The gRPC control links ride the Docker exec/attach
  stream, not a network port, so they work the same for local, Docker Desktop,
  Podman, and remote daemons. A launch may publish sandbox ports to the host via
  Docker port bindings (`container.ports` / `--publish`), which are set on the
  **netns** container at create time (Docker forbids them on a `container:`-networked
  tool container); a published service must bind `0.0.0.0` inside the sandbox.

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

The sandbox needs a Linux Toby binary to run the managers and the tool launcher.
The host delivers it **once per version** into a read-only `toby.bin.<key>` volume
(key = `version.Current` for a release, else `dev-<sha256[:12]>` of the binary):
the daemon creates the volume if absent, `docker cp`s the binary into it via a
throwaway container, then mounts it read-only at `/toby/bin` in every container — so
there is no per-container `docker cp`. On Linux the host copies its own running
binary from `/proc/self/exe`. macOS release builds embed a matching Linux helper;
local Darwin builds without the release embed tag require `TOBY_LINUX_TOBY` to point
at a Linux Toby binary.

## End-to-end launch flow

A direct launch such as `toby claude my-app` runs as a client against the daemon.
The client resolves the launch, sends a `session.start` request, and the daemon
acquires the netns unit + shared home, runs the tool lifecycle, and creates a tool
container the client attaches to:

1. **Plan execution.** Parse CLI flags, merge host-config sandbox defaults, and
   (if enabled) autoload `<project>/.toby.yaml`. The planning registry expands the
   requested tools to include declared dependencies and determines the primary
   (foreground) tool. The client marshals the resolved options/overrides into a
   `session.start` request and dials the daemon, spawning one if none is running.
2. **Acquire the netns unit.** The daemon keys the netns unit by label + project
   sources + home profile; if it is already up it reuses it, otherwise it builds the
   per-project+profile fx graph (frozen at the current config), ensures the bin
   volume + image, stands up the netns container, serves the `Tunnel` gRPC over its
   manager stdio, publishes the built-in Toby MCP server, and configures the MCP/
   provider proxies (upstreams registered with the host reverse proxy; the sandbox
   is told their proxied loopback URLs). The host phase (`toby.lifecycle.host.init`)
   registers the tool container's binds (docker socket, `~/.docker`).
3. **Acquire the shared home.** The daemon acquires the profile's home container
   (standing it up on first use with the same image + bin volume, chowning
   `/toby/home` once), giving a `SandboxClient` for files + streamed exec.
4. **Run the tool lifecycle (against the home).** Under the per-home install mutex,
   the daemon runs `toby.lifecycle.sandbox.install`/`upgrade` (each command via the
   home manager's `exec.run`, output streamed to the client as `install.output`),
   then `configure`, `context.setup`/`context.init` (context files written into the
   per-session `/toby/home/.toby/run/<sid>/` via `file.*`), and `init`. For
   `--install` the daemon runs install only and reports back with nothing to launch.
5. **Create the tool container.** The daemon writes a launch descriptor (argv, env,
   working dir) into the home volume and creates — but does not start — a tool
   container that joins the netns (`NetworkMode=container:<netns>`), mounts the home
   volume + bin volume + workspace binds, runs as the invoking `uid:gid`, and whose
   entrypoint is `sandbox launch <descriptor>`. It returns that container's id and
   whether the managed terminal is enabled.
6. **Attach and run (client).** The client attaches to the tool container and starts
   it; `sandbox launch` sets the env, `chdir`s, and `syscall.Exec`s the tool as PID
   1, so the PTY, raw mode, and approval modal attach to the user's real terminal.
   Approval prompts the daemon raises during the run round-trip to this client over
   `approval.prompt`. `SIGINT` (Ctrl-C) reaches the tool through its PTY.
7. **Release and idle teardown (daemon).** When the tool exits, the client sends
   `session.release` and exits with the tool's exit code. The daemon terminates the
   ephemeral tool container, removes the session's run directory, and drops the home
   + netns holds; when the last session for a netns unit is released it arms an idle
   timer (default 15m) and tears the netns + shared home down when it fires.
   `daemon.stop`, an explicit project stop, or idle auto-shutdown tear containers
   down the same way (under `--debug` a container is stopped but left on the host).

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
