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
  remain suppressible via `sandbox.suppressWarnings`.
- Match the surrounding code style; keep tests alongside the code they cover.

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
