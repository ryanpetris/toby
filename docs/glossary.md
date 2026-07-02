# Glossary

Canonical vocabulary for Toby. These terms have one meaning each — across code,
configuration, CLI, and docs. When code and prose disagree, this file wins; update the
code to match rather than redefining a term here.

## Core concepts

- **sandbox** — the isolated execution space a tool runs in. The user-facing and
  conceptual term. A single running sandbox is an *instance*. Use "container" only when
  talking about the Docker implementation detail specifically.
- **runtime** — the host-side machinery that creates and runs sandboxes. Docker is the
  only backend; Podman and remote daemons work through the standard `DOCKER_HOST`
  environment variable. There is no runtime selection. In code this is the
  `petris.dev/toby/sandbox/runtime` package.
- **engine** — the Docker-daemon layer beneath the runtime: the shared Docker client, the
  registry of containers Toby started, their deterministic teardown, and sanitized
  introspection. Code: pkg `container/engine`, `engine.Service`. Distinct from *runtime*
  (the pluggable sandbox backend) and *instance* (one running container).
- **environment** / **env** — the user's *named, persistent* context, e.g. `my-app` in
  `toby claude my-app`. It maps a host project to a persistent sandbox home volume of the
  same name. "Environment" is **not** a backend and **not** a Go interface — do not use
  it to mean the runtime.
- **instance** — one running sandbox (one container) for one session. Code: `Instance`,
  `BaseInstance`.
- **session** — one launch lifecycle, host and sandbox together, for a single
  `toby <tool> <env>` invocation. Code: `SessionState`.
- **launch** — the act of starting a session. **run** — the final *phase* in which the
  primary tool executes.
- **phase** — a step in the sandbox lifecycle: **prime → setup → run**.
- **primary tool** — the foreground tool a session launches (the one the user named).
  Do not call this the "foreground tool."

## Storage

- **mount** — the umbrella for attaching storage into a sandbox. A mount is either a
  *volume* or a *bind*.
- **volume** — a persistent, named Docker volume. The shared home is named
  `toby.<homeProfile>.runtime.home`.
- **bind** — a passthrough of a host path into the sandbox (e.g. the Docker socket,
  project directories). Not persistent state Toby owns.
- **home profile** — the shared `/toby/home` a launch runs under. Projects on the same
  profile share one home container + one home volume (installed tools and tool state
  persist and are shared across projects). Default is `default`; `settings.homeProfile`
  selects another.

## Integrations

- **tool** — a development tool Toby launches and manages (OpenCode, Claude Code, …).
  Code: the `tools.Tool` interface.
- **provider** — an upstream **AI API** endpoint (e.g. `anthropic`, `openai`). Reserved
  for this meaning only; never use "provider" for mounts or MCP.
- **MCP server** — a Model Context Protocol server Toby proxies to a tool.
- **sidecar** — a *local*, Toby-managed MCP server that runs as its own container for the
  session.

## Sides

- **host** — the user's machine running `toby`. Holds real credentials, host Git, the
  gRPC tunnel server, and the HTTP reverse proxy.
- **sandbox** — the isolated side where the tool runs. Prefer "sandbox" over "guest"
  (which Toby does not use) or "container" (Docker-specific).

## Structural suffixes (Go types)

- **Service** — the single fx-provided coordinator/owner of a package's state and
  resources. One per package.
- **Registry** — an in-memory collection/lookup of like items (e.g. `tools.Registry`).
- **Router** (or **Dispatcher**) — maps RPC method names to handlers.
- **Handler** — a type implementing a group of RPC methods.
