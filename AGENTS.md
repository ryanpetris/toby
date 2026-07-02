# Agent Guide

Orientation for agents (and humans) working on the Toby codebase.

## What Toby is

Toby is a single Go binary (`petris.dev/toby`, Go 1.26+) that runs development
tools inside private-home Linux sandboxes. The same binary runs on the host
(`toby <tool> <env>`) and inside the sandbox (`toby sandbox manager`, run as a
`docker exec` alongside the container's idle main process). The host talks to the
sandbox over a single gRPC connection carried on that manager exec's stdio, and
otherwise drives the sandbox through the Docker API (`docker exec`, `docker cp`).

Start with [docs/architecture.md](docs/architecture.md) for the full picture.
Other references: [docs/configuration.md](docs/configuration.md),
[docs/tools.md](docs/tools.md), [docs/examples.md](docs/examples.md),
[docs/sandbox.md](docs/sandbox.md), and
[docs/debugging-sandbox-startup.md](docs/debugging-sandbox-startup.md).

**Terminology is canonical.** [docs/glossary.md](docs/glossary.md) fixes the one meaning
of each core term (sandbox, runtime, environment, mount/volume/bind, tool, provider, MCP
server/sidecar, session, phase, …) and the structural-suffix convention below. Use those
words with those meanings in code, config, CLI, and docs; if code disagrees, change the
code, don't redefine the term.

## Temporary rules — pre-1.0 only

> **These rules apply only while Toby is pre-1.0** (`version.Current`
> below `v1.0.0`). They trade backward compatibility for a clean codebase while
> the API is still unstable, and **must be removed when Toby reaches `v1.0.0`.**
> If Toby has already reached `v1.0.0` and this section still exists, stop and
> tell the user that these rules are still here before making any change that
> relies on them.

- **Refactors and removals delete the old code completely** — as if it never
  existed. No compatibility shims, deprecation aliases, or fallbacks; no
  regression tests guarding removed behavior; no "is the old thing gone?"
  checks. Leave no trace or reference of the removed code anywhere — code,
  tests, or docs.
- **Freely change on-disk and interface shapes** to fit the functionality being
  added or changed: config file layout and keys, CLI flags and signatures, MCP
  tools and resources, and the like. Migration paths and backward-compatible
  fallbacks are not required.

## Package layout

Every package lives in its proper home, split by who may import it. A **top-level**
package is part of the public surface another module could build on if it used Toby
as a library: the `tools` plugin contract and its `helpers`/`kit`, the `sandbox`
interface and `sandbox/runtime`, `providers`, the container and context primitives,
the generic `platform`/`config/file` helpers, and the like. Everything that exists
only to assemble and run the Toby binary — the composition root, the CLI, the host
daemon and its thin clients, the client↔daemon and host↔sandbox control planes,
session orchestration, the concrete tool implementations, and build metadata —
lives under `internal/`, where the Go toolchain forbids any out-of-module import.

New packages start in the right half of that split: put it under `internal/` unless
an external consumer could genuinely build on it. Exported names follow
[the glossary](docs/glossary.md), dead exports stay unexported, and the public surface
is kept minimal. `main.go` imports only the composition root (`internal/app`) to
bootstrap.

## Build, test, and run

```sh
go build ./...
go test ./...
go vet ./...
gofmt -l .        # should print nothing
```

The project uses [uber-go/fx](https://github.com/uber-go/fx) for dependency
injection; `internal/app/module.go` is the composition root. `main.go` calls
`app.Run()`.

`version.Current` is overridden at build time with
`-ldflags "-X petris.dev/toby/internal/version.Current=<version>"`; release
builds are produced by `.github/workflows/release.yaml`.

`support/toby/Dockerfile` is the reference sandbox image used by the
repository's own `.toby.yaml`.

## Dependencies and licenses

[docs/dependencies.md](docs/dependencies.md) is the canonical inventory of every
Go module in `go.mod` (direct and indirect) and the license it ships under.

- **Permissive licenses only.** Toby may only depend on modules under permissive
  licenses (MIT, BSD-2/3-Clause, ISC, Apache-2.0, MPL-2.0, and similar). **Never**
  pull in a copyleft license — GPL, AGPL, LGPL, SSPL, or any license with
  comparable reciprocal/source-disclosure obligations — as either a direct or an
  indirect dependency.
- **Check the license before adding or updating a dependency.** Before running
  `go get`/`go mod tidy` to add or bump a module, verify the license of the new
  module **and** of any new indirect dependencies it drags in. Read the
  `LICENSE`/`COPYING` file in the module cache
  (`$(go env GOMODCACHE)/<escaped-module-path>@<version>/`). If anything is not
  clearly permissive, stop and do not pull it in.
- **Keep the inventory in sync.** Any change to `go.mod`'s `require` blocks —
  adding, removing, or updating a direct or indirect dependency — must update the
  matching row(s) in [docs/dependencies.md](docs/dependencies.md) in the same
  change, including whether the dependency is direct or indirect.

## Package map

The table is split by import boundary: **public** packages stay at the top level
because another module could build on them if it used Toby as a library; **internal**
packages live under `internal/` because they exist only to assemble and run the Toby
binary, and the Go toolchain forbids importing them from outside the module.

| Boundary | Area | Packages |
| --- | --- | --- |
| Public | Tools SDK | `tools` (`Tool` contract + `Registry`), `tools/helpers`, `tools/kit` |
| Public | Sandbox | `sandbox` (tool-facing sandbox interface), `sandbox/runtime` (host-side sandbox runtime + Docker backend), `container/engine`, `container/layout`, `container/mount`, `context/files` |
| Public | Providers | `providers` (+ `openai`, `anthropic`) |
| Public | Shared helpers | `config` (XDG path resolution), `config/file` (JSON/YAML decode + deep merge), `config/session` (resolved, sandbox-safe config handed to tools), `diagnostic/exitcode`, `diagnostic/warning`, `platform/environ` (env-var helper), `platform/executil` |
| Internal | Entry / wiring | `internal/app` (composition root + `main.go` entry; `daemon.go` dispatches `toby daemon`, `client_runner.go` is the client-backed session runner + transport selection), `internal/cli` (Cobra command tree), `internal/version` |
| Internal | Daemon / client | `internal/daemon` (the long-lived host process: `Service` accept loop, `daemon.*`/`session.*` control capabilities, per-project bring-up `Lifecycle`), `internal/daemon/project` (race-safe per-project `Registry` + container state machine), `internal/daemon/protocol` (dependency-free client↔daemon wire contract), `internal/daemon/transport` (+ `transport/unixsocket`, `transport/websocket`: the swappable client↔daemon transport seam and its two implementations), `internal/daemon/configwatch` (mtime/size config reload), `internal/daemon/resource` (daemon-root registry of shared, refcounted MCP sidecar backends — one container per configured local server, shared across projects), `internal/client` (thin client: dial/spawn daemon, run the foreground tool via `docker exec`, host the approval prompter) |
| Internal | Session | `internal/session/graph` (the shared per-project fx graph), `internal/session/run` (per-project `Container` held open across launches: `BringUp`/`Install`/`LaunchPlan`/`Close`), `internal/session/resolve` (privileged session-config resolver) |
| Internal | Config / context | `internal/config/app` (host config: deep-merges defaults with the user config), `internal/config/launch`, `internal/config/container`, `internal/context/setup` |
| Internal | Tools | tool implementations `internal/tools/builtin/<name>`; fx composition `internal/tools/wiring`; test double `internal/tools/fake` |
| Internal | Control plane | `internal/control` (JSON-RPC envelope + capability `Router` + host-identity sentinels, used in-process), `internal/control/tunnel` (gRPC-over-stdio transport + host proxy bridge), `internal/control/stdio` (net.Conn over stdio + one-shot listener), `internal/control/host` (router for the in-process git capability + shared reverse proxy), `internal/control/sandbox` (the proxy-only in-sandbox manager), `internal/control/methods/git` (the host-side git capability), `internal/control/httpproxy`, `internal/control/mcpproxy` (per-project MCP registration: acquires a lease on a shared `internal/daemon/resource` backend and registers it on the project's reverse proxy — the sidecar containers themselves live in the shared daemon-root registry), `internal/control/mcpserver` (host-side MCP server framework + per-session contract) & `internal/control/mcpserver/services/{git,session}` (the git and session-introspection tool/resource service plugins) |
| Internal | Lifecycle | `internal/lifecycle` (launch phase runner) |
| Internal | Sandbox delivery | `internal/sandbox/binary` |

There are two control planes. The **client↔daemon** channel (`internal/daemon/transport`) carries JSON-RPC over a swappable transport — unix socket (default) or WebSocket, selected by `TOBY_TRANSPORT` at the composition root — using the same `control.Peer` framing; both ends depend only on the `protocol` DTOs. The **host↔sandbox** transport is unchanged: the gRPC `Tunnel` service (`internal/control/tunnel`) carried over the manager exec's stdio (`internal/control/stdio`), now owned by the daemon **per project** — the container's main process is the idle `toby sandbox idle`, the sandbox manager is a proxy-only `docker exec`, and the daemon drives all sandbox operations via the Docker API (the client only runs the returned foreground exec). The remaining **capability** is host Git in `internal/control/methods/git`: it owns its wire contract (`types.go`, `contract.go`) and handler (`Service`), is provided into the `control.host.handlers` fx group via `fx.Annotate(asCapability, fx.As(new(control.Capability)), fx.ResultTags(...))`, and is dispatched in-process through the `control.Router` that `internal/control/host` builds from that group. Generic envelope helpers (`DecodeParams`/`DecodeResult`/`EmptyResult`) and shared sentinels (`HostUser`/`HostGroup`) live in `internal/control` itself.

## Conventions

- **Adding a tool:** create `internal/tools/builtin/<name>` and let it own its identity —
  declare a `Name` constant and a `Meta` (`tools.Metadata`) value in the package,
  then implement the `tools.Tool` interface (embed `tools.Base{Metadata: Meta}`
  for identity + no-op lifecycle defaults, or `kit.Simple` for a config-driven
  CLI). Provide it into the `tools` fx group via a `Module()` that provides
  `tools.Tool`. Register the tool by adding one `{Meta, Module}` row to the
  `entries` list in `internal/tools/wiring`; the planning metadata and the name→module
  selection are both derived from that list, so there is no central name constant.
  A tool that depends on another references the dependency's exported `Name` (e.g.
  `Dependencies: []string{npm.Name}`); the `Registry` orders tools by a
  topological sort of those dependencies (no priority numbers). Tool lifecycle
  phases (`PrepareHost`, `ConfigureSandbox`, `InitSandbox`, `Install`, `Launch`)
  are driven by the `lifecycle.Runner`; optional capabilities like writing context
  files are separate interfaces (`tools.ContextFileRegistrar`). Add tests next to
  the package and update [docs/tools.md](docs/tools.md) and the README tool table.
- **Synthetic config** belongs in the tool's `RegisterContextFiles` (and/or
  launch flags in `Launch`); never write into the tools' real config files on
  the host or in the sandbox home.
- **Warnings** must use a registered ID in `diagnostic/warning` so they
  remain suppressible via `settings.suppressWarnings`.
- Match the surrounding code style; keep tests alongside the code they cover.
- **Name getters without a `Get` prefix.** Follow idiomatic Go (Effective Go):
  a method that returns data the receiver holds takes the bare field-style name —
  `Mounts()`/`Mount(key)`, `DaemonClass()`, not `GetMounts()`/`GetDaemonClass()`.
  This holds for lazy-loading accessors too (one that ensures a value is loaded
  and then returns it). The `Get` *prefix* survives only where `GET` is the real
  operation, not an accessor — an HTTP GET helper (`GetJSON`). Related forms:
  - **setters keep `Set`** (`SetEnvironment`), and a getter/setter pair drops the
    prefix on the getter only (`Environment`/`SetEnvironment`);
  - a **map/registry-style lookup** may use a bare `Get`/`Lookup` matching the
    stdlib (`url.Values.Get`, `os.LookupEnv`) — it's the `GetX` *prefix* that's
    out, not the bare method name `Get`;
  - **constructors** (`New*`) and key/value builders (e.g. `RuntimeHomeKey`),
    **actions** that do real work or a side effect (`AddMount`, `Configure`,
    `Ping`), and lazy initializers that construct a resource on first call
    (`Client`) were never getters and are unaffected;
  - **pure computations** and transforms of their arguments (e.g. `Volume`,
    `Expand`) derive a result rather than read stored state;
  - **predicates** (`Is*`/`Has*`) and the `String()`/`Error()` interface methods.
- **No function aliases.** Don't add a function or method that exists only to
  forward to another under a second name (e.g. a `Volumes` that just returns
  `Mounts()`). Keep one canonical name and update every call site to use it.
- **Assert interface implementations at compile time.** When a type is meant to
  satisfy an interface, add a blank-identifier assertion in the same file as the
  type so a drifting method set fails the build instead of some distant call
  site: `var _ providers.Client = (*Service)(nil)` for pointer receivers, or
  `var _ Stringer = Kind("")` when value receivers suffice. Place it just after
  the type declaration. This is required when the interface lives in another
  package (e.g. an fx group member implementing a shared interface); for a type
  and the interface it implements in the same package it is encouraged but
  optional.
- **One concern per package, one concern per file.** A package covers a single
  high-level concern (`providers` is only about model providers; its `openai`
  and `anthropic` submodules hold only those backends' implementation). Within a
  package, split by concern across files: the fx wiring (`Module`, provider
  annotations, constructors it registers) lives in `module.go`; each class-like
  type — one with state and behavior, e.g. a `Service` or `Registry` — gets its
  own file named for it (`service.go`, `registry.go`); data-only structs and
  enums, or ones with only trivial data-manipulation helpers, collect in
  `types.go`. Split `types.go` (or any file) further the moment it spans more
  than one concern or grows unwieldy. Don't pile multiple concerns into one file
  just because the package is small.
- **Every file opens with a purpose comment.** The first thing in each file is a
  short comment stating the single concern that file covers. Exactly one file per
  package carries the package doc comment (`// Package <name> …`) describing the
  package as a whole; every other file places its comment immediately after the
  `package` clause, naming its concern and — where it helps a reader — summarizing
  the key type's contract. For example, `executil`'s runner file: "Runs commands
  on the host as subprocesses; `Runner.Run` executes a command and returns its
  exit code, forwarding stdio and signals."
- **Structural type suffixes follow the glossary.** `Service` is the single
  fx-provided coordinator of a package; `Registry` is an in-memory collection of
  like items; `Router`/`Dispatcher` maps RPC methods to handlers; `Handler`
  implements a group of RPC methods. Do not introduce new `*Manager` types — that
  suffix is retired. See [docs/glossary.md](docs/glossary.md).
- **Don't namespace fx value-group names.** A value group is keyed by the pair
  `(type, group-name)`, and the element type already disambiguates it, so a
  `toby.` prefix (or any package-path prefix) adds nothing — an external library
  cannot collide with a group whose element type is one of ours. Name a group for
  the glossary concept it collects: `group:"providers"`, not
  `group:"toby.providers"`. Any existing `toby.*` group name is legacy; when you
  touch that code, drop the prefix and rename the group to the glossary term for
  what it holds. (fx **module** names are cosmetic labels and need no prefix
  either, but they never collide, so they are lower priority.)
- **Group statements by concern.** Inside a function, separate logically
  distinct steps with a blank line so each group reads as one unit, instead of
  letting unrelated lines run together. A group is the set of lines that work
  toward one thing — usually centered on one variable or one operation. Rules of
  thumb:
  - Lines that work with the same variable belong together: its value, the error
    check, any later uses or assertions on it, and the `return` that yields it.
    Don't split those apart.
  - An assignment plus the `if` that consumes it is one group.
  - A `mu.Lock()` and its paired `defer mu.Unlock()` stay together as a group.
  - Consecutive plain assignments or declarations may share a group.
  - A bare terminal `return nil` / `return err` (one that doesn't yield a value
    the group above just built) may stand as its own group.

  Insert a blank line when the next lines move to a different variable or
  operation; keep the lines within a group together (no blank line inside it).
  Prefer fewer, meaningful groups over splitting every statement.

## Documentation sync rules

- When changing the host↔sandbox transport (the gRPC `Tunnel` service in
  `internal/control/tunnel`, or `internal/control/tunnel/tunnel.proto`), update
  [docs/architecture.md](docs/architecture.md) in the same change.
- When changing `toby mcp` / Toby MCP tools, arguments, or setup requirements,
  update the MCP section in `README.md` (and `docs/sandbox.md`) in the same
  change.
- When changing host or launch config keys, update
  [docs/configuration.md](docs/configuration.md) and the README config sections.
- When adding, renaming, or changing a tool's install or config injection,
  update [docs/tools.md](docs/tools.md) and the README tool table.
- When changing the runtime architecture or control flow, update
  [docs/architecture.md](docs/architecture.md).
- When changing `go.mod`'s dependencies (adding, removing, or updating a direct
  or indirect module), update [docs/dependencies.md](docs/dependencies.md) in the
  same change after confirming the license is permissive.
