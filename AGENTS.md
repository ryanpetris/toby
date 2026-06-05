# Agent Guide

Orientation for agents (and humans) working on the Toby codebase.

## What Toby is

Toby is a single Go binary (`petris.dev/toby`, Go 1.26+) that runs development
tools inside private-home Linux sandboxes. The same binary runs on the host
(`toby <tool> <env>`) and inside the sandbox (`toby sandbox manager`), talking
over JSON-RPC 2.0 on an authenticated WebSocket.

Start with [docs/architecture.md](docs/architecture.md) for the full picture.
Other references: [docs/configuration.md](docs/configuration.md),
[docs/tools.md](docs/tools.md), [docs/examples.md](docs/examples.md),
[docs/sandbox.md](docs/sandbox.md), and the control protocol schema in
[docs/toby-control-openapi.yaml](docs/toby-control-openapi.yaml).

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

## The `internal/dirty/` quarantine

Everything under `internal/dirty/` predates the terminology/API cleanup and has
**not** been reviewed against [the glossary](docs/glossary.md) — treat its exported
names, package boundaries, and structure as provisional and dirty. `internal/` is a
fine long-term home for genuinely private code; the `dirty/` prefix is the temporary
marker for "not yet cleaned."

As a package is cleaned (names aligned to the glossary, dead exports unexported, public
surface verified), **promote it out of `internal/dirty/`** to its proper home: a
top-level package when it is a clean public API, or a plain `internal/` package when it
should stay private. The goal is to empty `internal/dirty/`. Do not add new code under
`internal/dirty/` — new packages start clean in their proper location.

**Clean code must never reference dirty code.** Nothing outside `internal/dirty/` — no
top-level package and no plain `internal/` package — may import any
`internal/dirty/...` package. Dependencies flow one way only: dirty → clean, never
clean → dirty. A consequence is that you can only promote a package out of
`internal/dirty/` once everything it imports has already been cleaned and promoted, so
cleanup proceeds leaf-first. If promoting a package would force a clean package to
import the quarantine, it is not ready yet.

The sole tolerated exception is the root `main.go`, which must import the composition
root (`app`) to bootstrap; it stops referencing the quarantine when `app` is promoted
out, necessarily last.

## Build, test, and run

```sh
go build ./...
go test ./...
go vet ./...
gofmt -l .        # should print nothing
```

The project uses [uber-go/fx](https://github.com/uber-go/fx) for dependency
injection; `internal/dirty/app/module.go` is the composition root. `main.go` calls
`app.Run()`.

`version.Current` is overridden at build time with
`-ldflags "-X petris.dev/toby/version.Current=<version>"`; release
builds are produced by `.github/workflows/release.yaml`.

`support/toby/Dockerfile` is the reference sandbox image used by the
repository's own `.toby.yaml`.

## Package map

| Area | Packages |
| --- | --- |
| Entry / wiring | `internal/dirty/app` |
| CLI | `internal/dirty/cli/commands`, `internal/dirty/cli/launchconfig`, `internal/dirty/cli/session` |
| Config | `internal/dirty/config/toby` |
| Context injection | `internal/dirty/context/files`, `internal/dirty/context/setup` |
| Control handlers (dirty) | `internal/dirty/control/mcpserver`, `mcpproxy` |
| Sandbox runtimes | `internal/dirty/sandbox` (+ docker runtime) |
| Tools | tool implementations `internal/dirty/tools/<name>`; fx composition `internal/dirty/toolwiring`; shared `internal/dirty/tools/{toolutil,helpers,tooltest}` |
| **Clean (promoted)** | `config` (XDG path resolution), `config/file` (JSON/YAML decode + deep merge), `container/engine`, `container/layout`, `container/mount`, `control` (transport + JSON-RPC envelope + capability `Router`), `control/host` & `control/sandbox` (the two control endpoints), `control/methods/<name>` (one self-contained capability per method family: `files`, `env`, `command`, `git`, plus `lifecycle` method names), `control/httpproxy`, `diagnostic/exitcode`, `diagnostic/warning`, `lifecycle` (launch phase runner), `platform/environ` (env-var helper), `platform/executil`, `providers` (+ `openai`, `anthropic`), `sandbox` (tool-facing sandbox interface), `sandbox/binary`, `tools` (`Tool` contract + `Registry`), `version` |

A control **capability** lives in `control/methods/<name>`: it owns its wire contract (`types.go` for params/results, `contract.go` for method-name constants, request builders, and param/result decoders) and its handler (`Service`). The `Service` is provided to fx both as a concrete injectable type and into a handler group (`control.host.handlers` or `control.sandbox.handlers`) via `fx.Annotate(asCapability, fx.As(new(control.Capability)), fx.ResultTags(...))`; the host/sandbox registries build a `control.Router` from that group. Generic envelope helpers (`DecodeParams`/`DecodeResult`/`EmptyResult`) and shared wire sentinels (`HostUser`/`HostGroup`) live in `control` itself, not in any capability.

## Conventions

- **Adding a tool:** create `internal/dirty/tools/<name>`, implement the
  `tools.Tool` interface from the clean `tools` package (embed `tools.Base` for
  identity + no-op lifecycle defaults, or `toolutil.Simple` for a config-driven
  CLI), and register it into the `toby.tools` fx group via a `Module()` that
  provides `tools.Tool`. Wire that module into `internal/dirty/toolwiring`, and
  add its name constant + group in `tools/tools.go` / `tools/registry.go`. Tool
  lifecycle phases (`PrepareHost`, `ConfigureSandbox`, `InitSandbox`, `Install`,
  `Launch`) are driven by the `lifecycle.Runner`; optional capabilities like
  writing context files are separate interfaces (`tools.ContextFileRegistrar`).
  Add tests next to the package and update [docs/tools.md](docs/tools.md) and the
  README tool table.
- **Synthetic config** belongs in the tool's `RegisterContextFiles` (and/or
  launch flags in `Launch`); never write into the tools' real config files on
  the host or in the sandbox home.
- **Warnings** must use a registered ID in `diagnostic/warning` so they
  remain suppressible via `settings.suppressWarnings`.
- Match the surrounding code style; keep tests alongside the code they cover.
- **Prefix accessors with `Get`/`get`.** A function whose job is to return data
  the receiver already holds (or trivially derives from it) is named with a
  `Get` prefix — exported `GetX`, unexported `getX`. For example a service that
  hands back its registered mounts uses `GetMounts()`/`GetMount(key)`, not
  `Mounts()`/`Mount(key)`. This does **not** apply to:
  - constructors (`New*`) and key/value builders (e.g. `RuntimeHomeKey`);
  - functions that perform the real work or a side effect rather than merely
    read — actions (`AddMount`, `Configure`, `Ping`), and lazy initializers that
    construct and return a resource on first call (e.g. `Client`). A
    lazy-loading *accessor* — one that ensures a value is loaded and then returns
    stored data — is still a getter and takes the prefix (e.g. `GetDaemonClass`);
  - pure computations and transforms of their arguments (e.g. `Volume`,
    `Expand`), which derive a result rather than read stored state;
  - predicates (`Is*`/`Has*`) and the `String()`/`Error()` interface methods.
- **No function aliases.** Don't add a function or method that exists only to
  forward to another under a second name (e.g. a `GetVolumes` that just returns
  `GetMounts()`). Keep one canonical name and update every call site to use it.
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

- When changing the Toby control JSON-RPC protocol, update
  `docs/toby-control-openapi.yaml` in the same change.
- When changing `toby mcp` / Toby MCP tools, arguments, or setup requirements,
  update the MCP section in `README.md` (and `docs/sandbox.md`) in the same
  change.
- When changing host or launch config keys, update
  [docs/configuration.md](docs/configuration.md) and the README config sections.
- When adding, renaming, or changing a tool's install or config injection,
  update [docs/tools.md](docs/tools.md) and the README tool table.
- When changing the runtime architecture or control flow, update
  [docs/architecture.md](docs/architecture.md).
