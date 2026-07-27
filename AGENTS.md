# Agent Guide

Orientation for agents and humans working on the Toby codebase.

## What Toby is

Toby is three Go binaries that run development tools inside private-home
sandboxes:

- `toby` is the host launch and management CLI;
- `tobyd` runs the per-user agent; and
- `tobys` is the sandbox-only helper.

Use the Go version declared in `go.mod` (currently Go 1.26.3).

Toby is Linux-only. Each lifecycle or application command runs directly under
Bubblewrap from a verified OCI rootfs, per-user persistent home and tool
volumes, and a run-unique writable root overlay. The invoking CLI owns
foreground processes and terminal I/O; there is no idle sandbox or in-sandbox
supervisor. The `tobys` binary is mounted read-only at `/toby/bin/tobys` for
sandbox-only helper commands.

The per-user `tobyd` process owns versioned agent sessions,
independent OCI/MCP/models resource leases, and reusable background resources.
It listens on a host-only private agent socket, normally
`$XDG_RUNTIME_DIR/toby/agent.sock`, which must never be mounted into a
sandbox. The invoking launch client owns narrower run-scoped capabilities such
as `/run/toby/sandbox.sock`.

The root `.toby/config.yaml` selects a prebuilt OCI image. Toby does not build a
sandbox image at launch. The explicit built-in `docker` tool is the only Docker
integration and is a high-trust opt-in capability, not a runtime dependency.

Start with [docs/README.md](docs/README.md), then
[docs/architecture.md](docs/architecture.md) for the complete current design.
Other references:

- [docs/glossary.md](docs/glossary.md)
- [docs/configuration.md](docs/configuration.md)
- [docs/tools.md](docs/tools.md)
- [docs/examples.md](docs/examples.md)
- [docs/management.md](docs/management.md)
- [docs/sandbox.md](docs/sandbox.md)
- [docs/protocols.md](docs/protocols.md)
- [docs/debugging-sandbox-startup.md](docs/debugging-sandbox-startup.md)
- [docs/installation.md](docs/installation.md)

Terminology in [docs/glossary.md](docs/glossary.md) is canonical. Use its
definitions consistently in code, config, CLI output, and documentation. If
code and the glossary disagree, update the code rather than inventing a second
meaning.

## Temporary pre-1.0 rules

These rules apply only while Toby remains below `v1.0.0`. If Toby has reached
`v1.0.0` and this section still exists, stop and tell the user before making a
change that relies on it.

- When replacing or removing behavior, delete the old implementation completely
  as if it never existed.
- Do not add compatibility shims, deprecated aliases, dual config paths,
  fallbacks, or regression tests that merely verify removed behavior is absent.
- Remove obsolete code, tests, config, documentation, and dependencies together.
- On-disk formats, CLI shapes, MCP contracts, and configuration schemas may
  change directly when that produces the correct clean design.
- Keep the agent application protocol version at `1`; pre-1.0 protocol changes
  replace version-1 behavior directly instead of incrementing the version.

## Project structure

Packages are split by import boundary.

- Executable entry points belong beneath `cmd/toby`, `cmd/tobyd`, and
  `cmd/tobys`.
- Binary assembly and implementation details belong under `internal/`.
- New packages belong under `internal/` unless the project deliberately
  introduces and supports an external Go API.
- Keep exported surfaces minimal. An identifier may still need to be exported
  from an `internal` package for another internal package to use it.
- Each `cmd/<binary>/main.go` imports only its matching composition root beneath
  `internal/app`.

Current package map:

| Boundary | Area | Packages |
| --- | --- | --- |
| Commands | Entry points | `cmd/toby`, `cmd/tobyd`, `cmd/tobys` |
| Internal | Entry and CLI | `internal/app/{client,agent,sandbox}`, `internal/cli`, `internal/version` |
| Internal | Configuration | `internal/config`, `internal/config/file`, `internal/config/session`, `internal/config/{app,launch,mcp,mcpresource,models,ociresource}` |
| Internal | Session and lifecycle | `internal/session/run`, `internal/lifecycle`, `internal/toolfiles`, `internal/runtimeassets` |
| Internal | Tools | `internal/tools`, `internal/tools/helpers`, `internal/tools/kit`, `internal/tools/builtin/<name>`, `internal/tools/wiring`, `internal/tools/fake`, `internal/tools/runtimepath` |
| Internal | Providers | `internal/providers`, `internal/providers/openai`, `internal/providers/anthropic` |
| Internal | Run capabilities | `internal/hostaction`, `internal/hostaction/methods/git`, `internal/tobymcp`, `internal/tobymcp/services/{git,session}`, `internal/socketrelay` |
| Internal | Per-user agent | `internal/agent`, `internal/agent/{client,clientresource,progressio,protocol,resource,resourcelease,resourcelog,resourceprogress,server,socket}` |
| Internal | Gateways | `internal/sandboxgateway`, `internal/sandboxgateway/protocol`; `internal/mcpgateway` and its transport/resource subpackages; `internal/providergateway`, `internal/providergateway/{caddy,modelresource,wiring}` |
| Internal | Policy and presentation | `internal/approval`, `internal/permission`, `internal/status` |
| Internal | OCI and storage | `internal/oci` and its subpackages; `internal/storage` and its subpackages |
| Internal | Sandbox runtime | `internal/sandbox`, `internal/sandbox/layout`, `internal/sandbox/mount`, `internal/sandbox/bwrap` |
| Internal | Shared support | `internal/diagnostic`, `internal/diagnostic/exitcode`, `internal/diagnostic/warning`, `internal/executable`, `internal/resourcehash`, `internal/shutdown`, `internal/uuid` |

Toby intentionally uses uber-go/fx for dependency injection. Wiring belongs in
`module.go`; packages beneath `internal/app` compose the three applications. Do
not replace established fx wiring with globals or ad hoc service location.

Fx is the process-level composition mechanism. It constructs and injects
services, factories, and registries that may manage dynamic resources through
ordinary Go methods and contexts they own. Do not represent runs, private homes,
sandbox instances, commands, leases, MCP sessions, routes, or child processes
as Fx graph nodes, and do not create nested Fx applications for them.
`fx.Lifecycle` is for process-wide startup, shutdown, and final cleanup rather
than one hook per dynamic resource.

Sandbox applications reach launch-owned resources through the
`toby.sandbox.v1` gRPC service on a run-scoped Unix capability mounted at
`/run/toby/sandbox.sock`. Stdio integrations select one registered resource
with `tobys resource connect -- <client-resource-id>`. Host Git is implemented in
`internal/hostaction/methods/git`; reverse requests travel over the launch's
live agent session so Git credentials and approval authority remain in
the invoking CLI. Models applications receive only a loopback capability URL
and synthetic credential. Keep privileged host state out of sandbox-owned code
and never expose the agent, Caddy administration, or upstream endpoints.

## Build, test, and release

Run commands from the repository root:

```sh
make build
make go/install
make test
make vet
make check/fmt
```

`make build`, `make go/install`, `make test`, and `make vet` generate the gRPC
Go types before running their Go command. `make gen/grpc` performs generation
directly.
The generated files beneath `internal/gen/toby/{agent,sandbox}/v1` are
ignored and must not be committed. Generation requires the system Protocol
Buffers compiler; the Makefile installs the pinned Go plugins when needed.
`make release/check` validates `.goreleaser.yaml`, while
`make release/snapshot` builds local archives and packages beneath `dist/`.
GoReleaser itself is installed at the pinned Makefile version when needed.

For release configuration and local release artifacts:

```sh
make release/check
make release/snapshot
```

`make check/fmt` must print nothing. Use `make fmt` on changed Go files.

During development, run focused tests first when useful:

```sh
make gen/grpc
go test ./internal/session/run -run TestName -v
```

Before completing a code change, run the full build, test, vet, and formatting
checks above unless the environment makes a check impossible. Report any check
that could not run and why. Documentation-only changes may use direct
documentation and diff validation instead of the full Go suite.

Before creating any commit, run these mandatory analysis gates from the
repository root:

```sh
make check/staticcheck
make check/deadcode
```

`staticcheck` must exit successfully, and `deadcode` must produce no
unreachable-function reports in either the production executable graph or the
test-inclusive package graph. The dead-code gate also rejects internal packages
that are unreachable from the production binary unless the Makefile identifies
them as test-only. `deadcode` may exit successfully even when it reports
findings. The Makefile installs either Go tool when unavailable. The
documentation-only validation exception above does not waive these gates when
a commit is requested.

Release binaries are Linux `amd64` and `arm64` builds with `CGO_ENABLED=0`. Do
not add a required CGO code path or dependency unless the user explicitly
approves changing that property. Use the Make targets above as the canonical
project checks. GoReleaser produces binary-only tar archives plus Debian and
Arch Linux packages. Package-owned systemd assets live beneath
`packaging/systemd`; release output belongs only beneath `dist/`.

`version.Current` is overridden at build time with:

```text
-X petris.dev/toby/internal/version.Current=<version>
```

Release builds are defined in `.github/workflows/release.yaml`.

## Dependencies and licenses

[docs/dependencies.md](docs/dependencies.md) is the canonical inventory of every
direct and indirect module in `go.mod` and its license.

Toby accepts only Go module licenses allowed by project policy: permissive
licenses such as MIT, BSD, ISC, Apache-2.0, and similarly approved licenses,
plus MPL-2.0. Do not add GPL, LGPL, AGPL, SSPL, BUSL, Commons Clause, PolyForm,
proprietary, custom, unclear, or comparably reciprocal/source-disclosure Go
module dependencies.

Before adding or updating a module:

1. Select the version deliberately; do not blindly upgrade the whole module
   graph.
2. Inspect the candidate module's `LICENSE` or `COPYING` file in the Go module
   cache.
3. Inspect every newly introduced or changed indirect module as well.
4. Run `go mod tidy` after an accepted direct dependency change.
5. Review every `go.mod` and `go.sum` change.
6. Update the matching rows in
   [docs/dependencies.md](docs/dependencies.md), including direct/indirect
   classification, in the same change.

If a license is missing or ambiguous, stop before accepting the dependency.
This Go-module policy does not apply to external programs Toby invokes or OCI
images it pulls for use. Artifacts Toby redistributes still require review
under their distribution terms.

## Adding or changing tools

Each built-in tool owns its identity and implementation under
`internal/tools/builtin/<name>`.

To add a tool:

1. Declare the package's `Name` constant and `Meta` value, including an explicit
   human-readable `DisplayName`.
2. Implement `tools.Tool`, usually by embedding
   `tools.Base{Metadata: Meta}` or using `tools/kit.Simple`.
3. Expose a package `Module` fx option that provides `tools.Tool` into the
   `tools` value group.
4. Add one `{Meta, Module}` row to `internal/tools/wiring.entries`.
5. Express ordering through `Meta.Dependencies`, referring to another tool's
   exported `Name`. The registry performs a topological sort; do not add
   priority numbers.
6. Put tests beside the tool and update `docs/tools.md` and the README's short
   command catalog.

The lifecycle runner drives `PrepareHost`, `ConfigureSandbox`, `InitSandbox`,
and `Install`; `internal/session/run` invokes the primary tool's `Launch`.
Generated files and runtime assets use separate internal contributor contracts.
Keep optional behavior in separate capabilities rather than widening
`tools.Tool`.

Lifecycle sandbox commands inherit their tool's display-name status scope. Set
`sandbox.ExecOptions.Status` to a concise static action when the executable
basename is not sufficiently descriptive; never include command arguments,
paths, credentials, or other sensitive values.

Generated configuration is written as ordinary files at each application's
native private-home or tool-volume paths and atomically replaced on launch.
Never introduce a shared context directory, symlink indirection, or a parallel
compatibility path. Never modify the host user's real tool configuration.
Concurrent launches sharing the same persistent backing use last-launch-wins
behavior for generated files; do not serialize applications with a lock.

Executable installers, wrappers, templates, and other substantial assets belong
in dedicated files and should be embedded when appropriate. They are not tool
configuration.

Warnings must use a registered ID in `internal/diagnostic/warning` so users can
suppress them through `settings.suppressWarnings`.

## Go design and style

- Prefer simple, readable code and explicit control flow.
- Use the standard library before adding a dependency.
- Pass dependencies explicitly through constructors and established fx wiring.
- Keep interfaces small and defined around consumer needs.
- Avoid global mutable state and package initialization side effects unless the
  package contract genuinely requires them.
- Keep public APIs small; do not export an identifier speculatively.
- Use the Fx-provided `internal/diagnostic.Service` for process diagnostics.
  Bind one stable source logger per component, use structured key/value
  attributes, and select the level according to operational impact. Use the
  standard-library logger adapter only when an API requires `*log.Logger`.
- Write incidental Toby output, startup status, warnings, and diagnostics to
  stderr. Reserve stdout for foreground application output and the documented
  result of commands intended for scripting.
- Cleanup, optional progress, and optional log-persistence failures do not
  replace a successful result or a primary operation error. Emit them at debug
  level when a diagnostic logger is available. Use
  `diagnostic.DiscardError` when the error is intentionally unobservable.
  Required finalization that determines whether the requested operation
  completed may still return an error.
- Diagnostic output is non-failing and must never recursively report its own
  output failure. After foreground handoff, process diagnostics and status
  output remain suppressed so they cannot disrupt the application streams.
- Preserve strict validation and fail-closed security boundaries. Partial
  results are appropriate only where the behavior explicitly treats items as
  independent.

### Errors, contexts, and concurrency

- Handle errors explicitly and add useful operation context with `%w`.
- Use `errors.Is` and `errors.As`; do not compare error strings.
- Do not discard errors silently.
- Avoid `panic` in library code.
- Pass `context.Context` as the first parameter for request-scoped work.
- Do not store a caller-owned request context in a struct.
- A lifecycle owner may store a context it creates or explicitly owns, together
  with its cancel function, when that context represents component lifetime.
- Give every goroutine a clear owner, cancellation path, error path, and join or
  shutdown behavior.
- Do not use channels merely for style; use them when they make coordination
  clearer.

### Naming and APIs

Accessor methods do not use a `Get` prefix. Use `Mounts()` rather than
`GetMounts()`. Setters retain `Set`, and a getter/setter pair uses names such as
`Environment()` and `SetEnvironment()`.

The `Get` name remains valid when GET is the operation (`GetJSON`) or for a
map-style lookup matching standard-library conventions. Constructors,
side-effecting actions, computations, predicates, and `String()`/`Error()` are
not accessors. Generated code follows its generator's conventions.

Do not add a function or method whose only purpose is to forward to another
under a second name. Keep one canonical name and update callers.

When a type is intended to implement an interface from another package, add a
compile-time assertion beside the type:

```go
var _ providers.Client = (*Service)(nil)
```

### Packages and files

- Keep one high-level concern per package and one concern per file.
- Put fx wiring in `module.go`.
- Give each stateful behavior type its own appropriately named file, such as
  `service.go` or `registry.go`.
- Keep closely related data-only structs and enums in `types.go`; split the file
  when it stops representing one concern.
- Do not create new vague catch-all packages. Existing focused packages such as
  `internal/tools/helpers` are intentional and are not a reason to create a
  general `utils` package.
- Keep methods for one type together when practical.

Every Go file opens with a purpose comment. Exactly one file per package carries
the package documentation comment (`// Package <name> ...`). Other files put a
short concern comment immediately after the `package` clause. Generated files
are exempt from manual comment and organization rules.

Structural type suffixes follow the glossary:

- `Service` is the fx-provided coordinator for one package concern.
- `Registry` is an in-memory collection of like items.
- `Router` or `Dispatcher` maps methods to handlers.
- `Handler` implements a related group of requests.

Do not introduce new `*Manager` types. The suffix is retired.

Do not namespace fx value-group names with `toby.` or package paths. The element
type already disambiguates a group. Name the group for the concept it collects,
such as `tools`, `providers`, or `lifecycle`. When touching a legacy namespaced
group, rename it consistently.

Within functions, group statements by concern with blank lines:

- Keep an assignment, its error check, and the uses of that value together.
- Keep a mutex lock and its paired deferred unlock together.
- Separate the next unrelated variable or operation with a blank line.
- Do not split every statement mechanically; prefer a few meaningful groups.

### Static assets

Do not put substantial scripts, templates, schemas, generated configuration
fixtures, or other static payloads in Go string literals. Keep them in dedicated
files and use `//go:embed` where embedding is appropriate. Small protocol
constants and short messages may remain inline when that is clearer.

## Testing

- Keep tests beside the code they cover.
- Add tests for new behavior and bug fixes.
- Prefer focused, in-process tests that do not require root privileges or
  external services.
- Prefer a real lightweight implementation, temporary directory, fake clock, or
  small fake over a large mock framework.
- In new or modified tests, prefer `t.Context()` when a test-scoped context is
  appropriate. Do not churn unrelated tests solely to replace
  `context.Background()`.
- Use `testing/synctest` when it materially makes goroutine or timer behavior
  deterministic; it is not mandatory for every concurrent test.
- Re-run focused tests while developing, then run the full validation commands
  before completing a code change.

## Comments and source text

Comments describe current behavior in present tense. Exported package,
identifier, and API comments follow normal Go conventions and begin with the
name they document where applicable.

Code comments must stand alone for repository readers:

- Explain what the code does and why.
- Do not refer to private planning documents, review findings, gates, or
  temporary discussion context.
- Do not narrate removed behavior or mention old identifiers and flags.
- Avoid noisy comments that merely restate obvious code.

Prefer plain ASCII for new source text unless Unicode is required for
correctness or materially improves a user-facing diagram or terminal UI. Avoid
invisible or confusable characters and do not churn existing files solely to
replace intentional Unicode. Avoid emojis in code, comments, logs, tests, and
project documentation.

In Markdown, leave a blank line before lists and after headings. Put CLI
commands, paths, environment variables, and configuration keys in backticks.

## Documentation sync

Documentation is part of the implementation change:

- Sandbox capability, agent protocol, or reverse host-action changes update
  `docs/protocols.md` and the corresponding architecture or sandbox guide.
- `toby mcp` tools, arguments, resources, or setup changes update the MCP
  sections in `docs/configuration.md`, `docs/sandbox.md`, and
  `docs/protocols.md` as applicable.
- Host or launch configuration changes update `docs/configuration.md`.
- Tool installation, naming, or generated-config changes update
  `docs/tools.md`; update the README only when its short command catalog or
  getting-started path changes.
- Image, volume, agent-management, or resource-log CLI changes update
  `docs/management.md`.
- Runtime architecture or control-flow changes update
  `docs/architecture.md`.
- User-visible CLI commands, flags, paths, environment variables, defaults,
  diagnostics, and failure behavior update the relevant detailed docs page;
  keep the README limited to installation, basic use, and navigation.
- Every `go.mod` requirement change updates `docs/dependencies.md` after license
  review.

Verify documentation against implemented code, not only against the plan that
motivated a change.

## Git and self-review

- Inspect `git status` and the relevant diffs before editing.
- Preserve unrelated user changes in a dirty worktree.
- Do not create or switch branches unless the user asks.
- Do not stage or commit unless the user asks.
- Never commit changes authored by someone else without explicit direction.
- Do not add AI attribution, co-author lines, session IDs, or tool signatures to
  commit messages.

When asked to commit, use a concise summary that describes the behavior changed.
Add a body when future readers need constraints, tradeoffs, or important edge
cases.

Before completing a non-trivial task:

1. Review the full diff for correctness, accidental scope, stale comments, and
   documentation impact.
2. Run `git diff --check`.
3. Run the checks appropriate to the changed files.
4. Use an independent review when it is available and materially useful; it is
   not a substitute for your own review.
