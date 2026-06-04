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

## Temporary rules — pre-1.0 only

> **These rules apply only while Toby is pre-1.0** (`internal/version.Version`
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

`internal/version.Version` is overridden at build time with
`-ldflags "-X petris.dev/toby/internal/version.Version=<version>"`; release
builds are produced by `.github/workflows/release.yaml`.

`support/toby/Dockerfile` is the reference sandbox image used by the
repository's own `.toby.yaml`.

## Package map

| Area | Packages |
| --- | --- |
| Entry / wiring | `internal/app` |
| CLI | `internal/cli/commands`, `internal/cli/launchconfig`, `internal/cli/session` |
| Config | `internal/config`, `internal/config/file`, `internal/config/toby` |
| Context injection | `internal/context/files`, `internal/context/setup` |
| Control transport | `internal/control` (+ `hostmanager`, `sandboxmanager`, `httpproxy`, `mcpserver`) |
| Sandbox runtimes | `internal/sandbox` (+ `binary`) |
| Tools | `internal/tools/tool` (interface/registry) and `internal/tools/<name>` |
| Diagnostics / platform | `internal/diagnostic`, `internal/platform/executil`, `internal/version` |
| Providers | `internal/providers/openai` |

## Conventions

- **Adding a tool:** create `internal/tools/<name>`, implement the `Tool`
  interface (usually via `toolutil.Simple`), register it in
  `internal/tools/module.go`, and add its name constant + group in
  `internal/tools/tool/types.go`. Add tests next to the package and update
  [docs/tools.md](docs/tools.md) and the README tool table.
- **Synthetic config** belongs in the tool's `RegisterContextFiles` (and/or
  launch flags in `Launch`); never write into the tools' real config files on
  the host or in the sandbox home.
- **Warnings** must use a registered ID in `internal/diagnostic/warning` so they
  remain suppressible via `settings.suppressWarnings`.
- Match the surrounding code style; keep tests alongside the code they cover.
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
