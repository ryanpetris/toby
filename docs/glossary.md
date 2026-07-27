# Glossary

Canonical vocabulary for Toby. These terms have one meaning each — across code,
configuration, CLI, and docs. When code and prose disagree, this file wins; update the
code to match rather than redefining a term here.

## Core concepts

- **sandbox** — the isolated execution space a tool runs in. The user-facing and
  conceptual term. A single running sandbox is a *sandbox instance*. Use
  "container" only for a third-party format/API that uses that word or the
  explicitly selected Docker tool.
- **sandbox instance** — one Bubblewrap process tree created for a command.
  Lifecycle commands may use separate instances while sharing one run's plan
  and writable root overlay. Instances do not share a PID or mount namespace
  merely because they belong to the same run or private home.
- **runtime** — the host-side machinery that validates deterministic run plans
  and starts sandbox instances. Kernel, filesystem, and Bubblewrap support is
  determined by the real operation that needs it rather than a speculative
  probe. Bubblewrap is the runtime; Docker is not a runtime backend or fallback.
- **environment** / **env** — the user's *named, persistent* context, e.g.
  `my-app` in `toby claude my-app`. Together with the launch profile, it
  selects a *private home* but does not identify an application, sandbox
  instance, image, project set, or long-lived process. "Environment" is
  **not** a runtime backend or Go interface.
- **private home** — the `_data` directory of a `home` volume associated with an
  environment and profile. Multiple concurrent runs may bind the same private
  home.
- **run** — one complete `toby <tool> <env>` CLI lifecycle: resolved image,
  private home, mount and generated-file plan, agent session and
  resource leases, lifecycle sandbox instances, and one primary application. A
  run owns a unique writable root overlay.
- **session** — protocol state belonging to one logical exchange. An agent
  session belongs to one client-opened `OpenSession` stream, and its
  loss releases every associated resource lease. An MCP session belongs to one
  connector exchange. Use *run* for the CLI launch lifecycle.
- **launch** — the act of starting a run or, when naming a lifecycle operation,
  starting its primary application. Do not use it for an agent-owned background
  process.
- **phase** — one ordered operation within a run, such as host preparation,
  sandbox configuration, initialization, installation, or primary launch.
- **primary tool** — the application tool a run launches in the foreground (the
  one the user named). Use this term when distinguishing it from selected
  dependency or context tools.
- **image** — an OCI manifest or index reference together with its verified
  content.
- **image reference** — one mutable, per-user mapping from a normalized OCI
  reference and exact platform to an immutable image object. Multiple image
  references may select the same object.
- **image object** — one immutable, per-user OCI layout, runtime bundle,
  rootfs, and Toby metadata document identified by platform and verified
  manifest digest. An object with no image references is *dangling*.
- **rootfs snapshot** — an immutable, per-user unpacked filesystem tree derived
  from one OCI manifest digest and platform. It is a Bubblewrap overlay lower,
  never a private home.

## Storage

- **volume** — one persistent, Toby-owned native storage object beneath
  `$XDG_DATA_HOME/toby/volumes/<id>`. Its canonical `metadata.json` determines
  its BLAKE2b-512 ID and `_data` contains the volume contents.
- **home volume** — a volume with type `home`, identified by the environment
  name and launch profile. Its `_data` directory is the private home.
- **tool volume** — a volume with type `tool`, identified by the tool name,
  purpose, and profile. It is global to the host user rather than nested
  beneath a private home.
- **profile** — the user-selected name that chooses the home volume and a set
  of global tool volumes. `settings.profile` supplies the home and launch
  default; `tools.<name>.profile` may override one tool without changing the
  home.
- **mount** — the umbrella for attaching a native host path into a sandbox. A
  mount plan contains tool-volume data directories and external binds;
  generated files are not mounts.
- **managed directory** — the runtime mount declaration that attaches one tool
  volume's `_data` directory at a tool-requested sandbox path. It does not
  introduce an additional storage scope or directory hierarchy.
- **bind** — an explicit host path attached at a sandbox target, such as a
  project directory. Unlike a managed directory, Toby does not own an external
  bind's contents.
- **root overlay** — one run's unique writable Bubblewrap upper/work pair over
  an immutable rootfs snapshot. It may be reused by that run's sequential
  lifecycle commands but never by concurrent runs.

## Integrations

- **tool** — a development tool Toby launches and manages (OpenCode, Claude Code, …).
  Code: the `tools.Tool` interface.
- **host socket relay** — a run-scoped Unix endpoint that proxies
  one selected host socket through the launch process without mounting the
  host socket. The launch process uses its host credentials for upstream
  connections; rootless Bubblewrap leaves inherited supplementary kernel GIDs
  unmapped in the sandbox, so path omission remains the authority boundary.
  Code: `internal/socketrelay`.
- **models resource** — one configured LLM API endpoint, protocol, and set of
  protected headers. Toby discovers the model document from that endpoint.
  Toby-owned configuration, protocol, and CLI surfaces use `models`;
  tool-native files may use `provider` when that tool requires the term.
- **MCP server** — a Model Context Protocol server Toby proxies to a tool.
- **agent resource** — one stable agent entry for the canonical effective
  configuration of an OCI image, MCP server, or models API. It has an opaque
  resource ID and may have multiple independent resource leases.
- **background resource** — a reusable process or transport generation behind
  one agent resource. It remains only while a stream or startup needs it and
  during its warm-idle timeout; a resource lease registers how to restart it
  but does not keep the generation running.
- **background service** — a background resource implemented by a long-running
  process. Local HTTP MCP sidecars and the models gateway use this lifecycle.
- **resource lease** — one independently releasable authority returned by the
  agent for one agent resource. An agent-session loss releases all leases
  belonging to that session. A resource lease never locks a private home or
  prevents a foreground launch.
- **service lease** — an internal background generation's revocable reference
  to a process. It is subordinate to agent resource demand and is not exposed
  by the agent protocol.
- **capability socket/route** — a random, revocable, launch-scoped endpoint that
  exposes only client resource UUIDs or a synthetic credential and no real
  upstream credential. The launch implements its sandbox resource gRPC service
  as an exact Unix-socket generation mounted at `/run/toby/sandbox.sock`;
  models also use a loopback HTTP route.
- **client resource ID** — a per-launch UUID generated by the launch CLI and
  placed in sandbox-visible configuration. The agent never uses it as a
  resource identity.
- **resource ID** — the agent's opaque stable handle for one agent resource.
  The current server represents it as a deterministic UUIDv8, but clients do
  not parse, derive, or recompute it.
- **lease ID** — the agent's opaque authority for one resource lease.
- **correlation ID** — an opaque UUID generated by the side initiating a
  request. The launch generates agent-API IDs; the agent generates reverse
  host-action IDs. Each associates exactly one request with all of its response
  messages or stream events.
- **operation ID** — an opaque identity for one bounded startup, preparation,
  discovery, or log operation.
- **MCP connector** — one stdio byte stream opened with
  `tobys resource connect -- <client-resource-id>` through a launch's sandbox
  resource service. A connector selects exactly one registered UUID and owns
  one MCP protocol session; it is not a reusable sidecar process.
- **sidecar** — the term reserved for a local MCP server managed in its own
  sandbox. Stdio MCP sidecars are one process per connector; matching HTTP MCP
  targets may share one process while
  keeping connector sessions separate. A sidecar is not a generic synonym for
  a container.

## Sides

- **host** — the user's machine running `toby`. It holds real credentials,
  native persistent storage, the launch CLI, and the per-user agent.
- **launch CLI** — the invoking `toby` process. It owns the run, foreground
  terminal and Bubblewrap children, live Git/approval authority, agent session,
  resource-lease translations, and sandbox-facing capabilities.
- **agent** — the per-user background `tobyd` process. It owns
  agent sessions, agent resources, resource leases, disk-backed logs, and
  reusable MCP/models generations, but never foreground application processes,
  terminal stdio, or sandbox-facing capability sockets.
- **sandbox** — the isolated application or sidecar side. Prefer "sandbox" over
  "guest" or "container."

## Structural suffixes (Go types)

- **Service** — the single fx-provided coordinator/owner of a package's state and
  resources. One per package.
- **Registry** — an in-memory collection/lookup of like items (e.g. `tools.Registry`).
- **Router** (or **Dispatcher**) — maps RPC method names to handlers.
- **Handler** — a type implementing a group of RPC methods.
