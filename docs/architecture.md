# Architecture

Toby is three Linux Go binaries with distinct process roles:

- `toby` is the launch and management CLI, which resolves a run and directly
  owns its Bubblewrap processes and terminal;
- `tobyd` runs one per-user agent, which owns versioned agent sessions, stable
  resource identities, independent leases, and reusable background resources;
  and
- `tobys` is the sandbox-only helper mounted at `/toby/bin/tobys`.

There is no persistent sandbox process. A lifecycle command or foreground
application is a separate Bubblewrap child of the invoking CLI. Sequential
commands in one run share the same private home, mounts, and writable root
overlay, but each command receives a new process and PID namespace. Before the
next command may reuse that overlay, the executor waits for the prior sandbox
init to exit and then releases its retained mount-namespace descriptor. If the
kernel has not yet completed deferred OverlayFS cleanup, Toby retries only the
next command's proven pre-payload setup window.

## High-level flow

```text
launch CLI
  |
  |-- request agent information, select a protocol, and open a session
  |-- resolve config, project set, tools, and resource definitions
  |-- present startup progress
  |     |-- interactive: clearable current-operation pane
  |     |-- redirected/debug: append-only output
  |     `-- quiet: no non-foreground output
  |-- register and prepare required OCI images through independent RPCs
  |-- acquire native storage
  |-- register one retained agent lease per MCP and models resource
  |-- create launch-owned sandbox and models capabilities
  |-- write generated files at native private-home paths
  |-- run setup commands under Bubblewrap
  `-- run foreground tool under Bubblewrap <--> user terminal

application sandbox
  |
  |-- stdio: tobys resource connect -- <client-resource-uuid>
  |             |
  |             `-- toby.sandbox.v1 gRPC on /run/toby/sandbox.sock
  |
  `-- HTTP: launch-client models URL + synthetic credential

per-user agent
  |
  |-- private agent socket (host only)
  |-- gRPC agent API + independent logical streams
  |-- stable resource registry + independent leases
  |-- disk-backed OCI/MCP/models operation logs
  |-- local and remote MCP backends
  `-- shared Caddy models gateway
```

The agent socket is never mounted into a sandbox. The application receives
only narrower, revocable capabilities created for its run.

Startup presentation is launch-owned and is separate from application I/O.
The foreground process always receives the original stdin, stdout, and stderr
objects so terminal detection, raw mode, resizing, job control, signals, and
approval prompts do not depend on the presentation mode.

Startup operations form a presentation hierarchy. A tool lifecycle action owns
a display-name scope and is the parent of its direct Bubblewrap commands. The
interactive renderer shows the running leaves: a command temporarily replaces
its parent row and supplies the active transcript, then the parent returns when
the command completes. Command labels describe the intended operation rather
than exposing executable names, scripts, or other implementation details.
Independent roots such as concurrent OCI preparations remain visible together.

## Process ownership

### Launch CLI

The invoking `toby` process owns:

- configuration and project resolution;
- OCI rootfs and native-storage leases;
- exact opened filesystem capabilities used to render Bubblewrap arguments;
- the run's writable overlay;
- lifecycle and foreground Bubblewrap children;
- terminal stdin, stdout, stderr, signal forwarding, and managed-PTY state;
- host Git execution and interactive approval authority; and
- the agent session, resource-lease translations, sandbox resource
  socket, and models loopback capability for that launch.

Foreground bytes do not pass through the agent. This keeps ordinary command
execution equivalent to a direct child process and avoids a long-lived
application supervisor.

### Per-user agent

`tobyd` owns:

- a private Unix listener, either created at
  `$XDG_RUNTIME_DIR/toby/agent.sock` or inherited from systemd at the normal
  path or `/run/toby/users/<username>/toby/agent.sock`;
- connection-owned agent sessions and resource leases;
- canonical BLAKE2b-512 identities for effective OCI, MCP, and models
  configuration;
- disk-backed resource operation and generation logs;
- reusable background-resource generations and idle teardown;
- remote MCP transport state;
- reusable local HTTP MCP sidecars; and
- the Caddy models gateway and its authorization state.

The agent does not receive the launch process's SSH agent, GPG agent, Git
configuration, terminal, or foreground process. Typed host-action messages for
host Git travel over the client-opened `OpenSession` stream and are handled by
the launch process. They are emitted only after a sandbox request reaches the
launch connector and is forwarded to the agent resource serving that request.

`toby agent stop`, `SIGINT`, and `SIGTERM` begin the same bounded agent
shutdown. The agent sends each connected launch a shutdown request with
a 17-second client deadline while retaining a private 20-second deadline. A
launch sends `SIGINT` to its exact foreground payload, allows 12 seconds for
graceful exit, and retains five seconds for bounded process-group escalation
and local scheduling. It then releases launch-owned capabilities and leases,
acknowledges the request, and closes its session. The agent drains gRPC for at
most five seconds and tears down agent-owned resources for at most ten seconds.
A systemd-owned socket can activate a new agent on a later connection.

Each approved host Git request self-executes the exact current `toby` binary
through `/proc/self/exe` in a private supervisor mode dispatched before Fx and
Cobra startup. The launch passes an exact repository descriptor, a sealed
bounded argument document, direct anonymous output files, and a lifetime pipe;
the supervisor otherwise inherits the host environment, stdin, terminal
session, and supplementary credentials needed by Git. It becomes a Linux child
subreaper, signals exact processes through pidfds, and repeatedly kills and
reaps adopted descendants until none remain, including detached and
double-forked helpers. Cancellation or launch-process death closes the lifetime
pipe. The host-action handler does not read its output files until the
supervisor and its descendants have been reaped.

### Bubblewrap children

Each lifecycle, sidecar, or application process runs from a deterministic
Bubblewrap plan. The application process gets:

- fresh user, PID, IPC, and UTS namespaces;
- the host network namespace for the initial implementation;
- an immutable OCI rootfs covered by a run-unique writable overlay;
- `/proc`, a controlled `/dev`, tmpfs `/tmp`, and a fresh `/run`;
- the persistent private home at `/toby/home`;
- selected projects beneath `/toby/workspace`;
- resolved tool volumes and explicit external binds;
- transient runtime assets beneath `/run/toby`; and
- the `tobys` sandbox helper at `/toby/bin/tobys`, read-only.

Application capabilities are dropped. Namespace-root lifecycle commands receive
only the limited ownership and identity capabilities needed by installers.
Each binary rejects host UID zero before initializing its application.
The sandbox helper accepts namespace UID zero only when its UID mapping selects
a nonzero parent UID.

## Static composition and dynamic runs

`internal/app/client` and `internal/app/agent` each build one process-wide Fx
graph for their binary. The client graph provides configuration, tool
registries, the Bubblewrap facade, the agent client, lifecycle services,
approval, status, and the native session runner. The agent graph provides the
agent server, resource registry, MCP and models gateways, and their background
lifecycle services. `tobys` dispatches its two fixed helper commands without an
Fx graph.

Runs, resource leases, sidecar generations, commands, overlays, sockets, and
private homes are ordinary values created by those services. They are not Fx
graph nodes, and Toby does not create nested Fx applications.

## Diagnostics and operation results

Fx constructs one process-wide diagnostic service. Components obtain a logger
bound to a stable source and emit `log/slog` records with structured key/value
attributes. `TOBY_LOG_LEVEL` selects `debug`, `info`, `warn`, or `error`;
`TOBY_LOG_FORMAT` selects compact `source: message` text or JSON records.
Registered warnings write through the non-failing diagnostic stderr sink.
Warning-level records are reserved for registered, user-suppressible
conditions. Their text begins with `warning[<id>]:`, and their structured
records include `warning_id` plus condition-specific attributes. An unexpected
failure of a process-wide capability that is not returned directly to a caller
uses the error level. A request or connection failure already represented by
its returned error, HTTP response, or closed stream uses the debug level for
its additional diagnostic record.
Text startup presentation writes to the original stderr stream so an
interactive renderer retains the stream's terminal identity; structured
startup records use the diagnostic logger.

Toby reserves stdout for foreground application output and documented command
results intended for scripts. Startup presentation, incidental output,
warnings, and diagnostics use stderr. Foreground handoff permanently
suppresses the diagnostic sink for that CLI process before the application
receives its original streams. Quiet launch mode suppresses the same sink
before startup work begins.

An error returned from an operation describes whether that requested operation
completed. Required publication, synchronization, process reaping, stream
delivery, and terminal-state restoration may therefore fail the operation.
Cleanup after a completed operation, optional progress delivery, diagnostic
rendering, and optional log persistence instead emit debug diagnostics and do
not replace a successful result or its primary error. Intentional discards
without an available diagnostic sink pass through the package-level
`diagnostic.DiscardError` hook. Diagnostic writes never return an error and do
not recursively diagnose their own failures.

For foreground sandboxes, output draining, terminal restoration, signal
delivery, structured-status completion, and mount-namespace finalization are
required finalization. Descriptor disposal, canceled input-reader closure, and
terminal-shadow teardown after required restoration are cleanup and cannot
replace the foreground result.

## Launch sequence

One production launch proceeds in this order:

1. The CLI parses enough of the invocation to select a launch command. Help,
   version, image and volume management, and the config-free agent commands
   (`status`, `stop`, `resources`, and `logs`) do not load launch
   configuration. Agent models and cache commands resolve the named models
   resource from host configuration.
2. The launch opens or starts the per-user agent, invokes `Hello`, selects the
   application protocol advertised by `HelloResponse`, and opens a persistent
   bidirectional `OpenSession` stream.
3. Host configuration and launch overrides produce an immutable effective
   configuration. MCP and models blocks are decoded, substituted, defaulted,
   and validated only after hello completes.
4. Project declarations are resolved with their origin: relative direct
   sources resolve from `XDG_PROJECTS_DIR`; containment is enforced unless the
   global external-project setting is enabled. Explicit launch-file paths may
   select external directories. The tool registry computes the selected
   dependency closure.
5. Toby resolves the Bubblewrap executable from `PATH`. The actual lifecycle
   and foreground commands establish the requested namespaces, mounts,
   process-group signaling, and terminal mode. Unsupported kernel or
   Bubblewrap behavior fails at the operation that needs it and Bubblewrap's
   diagnostic remains available in debug output.
6. Toby constructs one effective OCI configuration for the application
   rootfs, every local MCP image, and the Caddy image when models resources are
   configured. The application source may be a registry reference, an OCI
   image-layout archive, or a Dockerfile build; background resources use
   registry references. Each acquisition is independent. The agent defaults
   and validates the configuration again, computes its stable identity, and
   returns an opaque resource ID and lease ID. The launch opens a `PrepareOCI`
   stream for each required image.
   Independent images prepare concurrently, while matching requests join one
   agent operation. Toby materializes a verified OCI layout by pulling a
   registry reference, extracting an archive, or invoking `buildah` with an
   OCI output, then rootlessly extracts the selected rootfs. Absolute progress
   checkpoints and diagnostics are written to disk before they are sent to
   connected clients.
   The status service receives complete absolute snapshots and keeps only its
   bounded current presentation state and command transcript. Interactive
   rendering combines active OCI work beneath one shared status line and gives
   each image a transfer line with its phase, a Rich-style colored bar,
   percentage, and common-unit byte column. Only rendered transfer lines bound
   image references to 36 columns by removing complete left-hand path segments
   before right truncation. Once preparation finishes, the temporary agent
   OCI leases are released; the launch opens the published application rootfs
   with a native shared object lease and `pull: never`.
7. Toby opens exact project-directory descriptors. Sources whose resolved
   policy requires project-root containment are proven by descriptor ancestry
   to be at or beneath one retained `XDG_PROJECTS_DIR` descriptor. Toby then
   creates the tool-facing declaration facade with the image environment and
   selected foreground mode.
8. The lifecycle `PrepareHost` phase collects tool-volume and external-bind
   requests without entering a sandbox. Native storage resolves the home
   volume and requested tool volumes, and Toby creates the run-unique overlay.
9. Toby builds a bounded sandbox-safe session snapshot and host-action router,
   then independently acquires every models and MCP resource. Models discovery
   runs through the acquired models resources so generated tool files contain
   current model metadata. Registration remains lazy for MCP backends. Toby
   creates the generic run-scoped sandbox resource capability and loopback
   models capability; per-launch client resource UUIDs map to the opaque agent
   resource and lease IDs.
10. `ConfigureSandbox` freezes tool environment and argument-dependent
    declarations. Toby collects complete native tool files, transient runtime
    assets, and socket-relay declarations.
11. Toby starts any declared host socket relays, adds the host resolver as a
    read-only application bind, assembles the descriptor-owned Bubblewrap run,
    validates the complete plan, and atomically publishes the generated tool
    files at their native paths.
12. `InitSandbox` and `Install` run as direct Bubblewrap commands using the
    run's shared overlay.
13. Toby clears interactive startup presentation, then the primary tool
    launches under the selected terminal mode with the original process
    streams.
14. On exit, Toby closes launch-owned capabilities and resource streams,
    releases each resource lease, terminates owned children, releases storage
    and rootfs leases, and removes transient run data.

Any failure unwinds already-acquired resources in reverse order.

The process-wide shutdown service owns `SIGINT` and `SIGTERM`. During startup,
a signal records a pending stop, allows the active top-level operation or tool
lifecycle action to finish, and stops before the next checkpoint. Lifecycle
Bubblewrap children therefore complete the operation that published or mutated
state. Once the foreground Bubblewrap process group exists, signal ownership
transfers atomically to an exact pidfd supplied by the trusted payload shim.

An operating-system signal gives a launch 20 seconds to unwind. A
agent-stopping request uses the agent's advertised deadline instead. The
exact payload receives client-mediated operating-system signals; a
agent-stopping request sends it `SIGINT`. Under the agent's 17-second
advertised deadline, the payload has 12 seconds to exit after `SIGINT`. If it
remains alive, Toby sends `SIGTERM` to the complete Bubblewrap process group,
waits two seconds, sends `SIGKILL`, and waits up to two more seconds for reap,
retaining the final second for local scheduling before the client cleanup
deadline.

## OCI storage

The OCI implementation under `internal/oci` combines embedded Go libraries
with an optional Buildah process:

- `go-containerregistry` resolves registry references, selects the requested
  platform, pulls and verifies content, and writes a standard OCI layout;
- archive sources are extracted into a private temporary OCI layout with
  traversal and entry-type checks;
- build sources invoke `buildah build` with layer caching and a transient
  `oci-archive:` destination beneath `$XDG_CACHE_HOME/toby/images/builds`,
  then read and remove that OCI image-layout tar;
- Toby reads and digest-checks only the bounded manifest and image
  configuration it needs for sandbox planning;
- umoci's Go packages rootlessly unpack the layout into an OCI runtime bundle;
- Toby publishes the complete layout, bundle/rootfs, and schema-1 metadata as
  one immutable object, then atomically updates the mutable reference mapping;
  and
- prepared images retain an opened descriptor for the exact published rootfs.

Per-user image data lives beneath
`${XDG_DATA_HOME:-~/.local/share}/toby/images`. Fetch and materialization are
coordinated across concurrent launches with per-reference and per-object locks,
so unrelated images can prepare concurrently. `sandbox.pull` controls source
materialization: `if-missing` reuses a published reference, `always`
rematerializes its source, and `never` requires an existing reference. Registry
and archive sources require no external extraction executable. Build sources
require the host `buildah` command.

Archive configurations derive a deterministic internal reference from the
resolved archive path and platform. Build references use
`toby.local/<profile>/<primary-project>:<source-hash>`; the source hash covers
the resolved context and Dockerfile paths and platform. OCI-incompatible
profile or project names receive a deterministic safe repository component.
These identities do not include file contents. An `if-missing` launch with a
published reference therefore bypasses the archive or Buildah process and
rootfs extraction. An `always` launch rematerializes the source and atomically
updates the reference.

The on-disk catalog separates normalized reference records from immutable
objects. A reference identity includes the normalized registry reference and
exact platform and atomically points to one object. An object identity includes
the platform and verified manifest digest and contains the OCI layout,
bundle/rootfs, and schema-1 metadata. Multiple references can point to one
object. Removing a reference therefore does not remove a still-aliased object;
removing the final reference makes the object eligible for deletion.

The published shape is:

```text
images/
  references/<reference-key>.json
  objects/<platform-key>/sha256/<manifest-hex>/
    metadata.json
    layout/
    bundle/rootfs/
  locks/
  tmp/
```

`reference-key` is SHA-256 over the canonical normalized reference and platform
document. `platform-key` is SHA-256 over the normalized platform JSON. The
reference record stores `schema_version: 1`, the normalized reference,
platform, and relative object key. Object metadata stores
`schema_version: 1` and the verified platform, manifest/config descriptors, and
OCI runtime environment, workdir, entrypoint, command, user, and labels. Locks
and data-store temporary paths are implementation coordination state, not
published image identities. The intermediate Buildah archive lives separately
under `$XDG_CACHE_HOME/toby/images/builds` and is removed after it is read.

Every `Prepared` rootfs retains a shared advisory object lease for its complete
lifetime. A running sandbox can therefore continue using an object after a
forced final-reference removal, but the object remains dangling instead of
being unlinked. Object deletion takes a non-blocking exclusive lease. Normal
final-reference removal fails when that lease is unavailable; forced
reference removal leaves the busy object intact. Force never overrides the
object lease itself.

`toby image build` invokes `buildah` directly and writes an OCI archive without
publishing it. `toby image import` registers one agent OCI resource for an
archive and explicit reference. `toby image pull` registers one agent OCI
resource per registry input. Both agent-owned paths follow the existing
disk-backed preparation stream. Independent inputs run concurrently, and
identical requests attach to one agent operation. Launch clients create a
visible status row only after a preparation stream reports progress, output, or
failure. An immediate cached completion remains silent, while a client joining
active work receives the current snapshot and displays its progress.
`toby image list` presents one row per reference plus one row per dangling
object. `inspect` serializes catalog and runtime metadata as YAML or JSON, while
`path` prints only the kernel-resolved rootfs path. `remove` accepts selectors
or exact filters and uses the same Bubble Tea confirmation and persistent
completion rows as volume management. `prune` selects dangling objects only
and never removes an object with a live shared lease.
See [management.md](management.md#oci-images) for the complete image command
surface.

## Native storage

Persistent state is ordinary per-user filesystem data:

- home and tool volumes:
  `${XDG_DATA_HOME:-~/.local/share}/toby/volumes`;
- OCI data:
  `${XDG_DATA_HOME:-~/.local/share}/toby/images`; and
- transient overlays:
  `${XDG_CACHE_HOME:-~/.cache}/toby/runs`.

Every volume has exactly this Docker-like shape:

```text
volumes/<id>/metadata.json
volumes/<id>/_data/
```

`<id>` is the lowercase BLAKE2b-512 digest of the canonical schema-1 metadata
document. A home volume's identity contains `type: home`, the exact environment
name, and the selected launch profile. A tool volume's identity contains
`type: tool`, the registered tool name, the tool-defined purpose, and its
selected profile. The default profile is `default`; a launch may select another
profile for its home and default tool volumes and may override it for
individual tools. Tool volumes are therefore reusable across private homes and
projects when their identity metadata matches.

Canonical metadata has these shapes:

```json
{"schema_version":1,"type":"home","name":"my-app","profile":"default"}
{"schema_version":1,"type":"tool","name":"opencode","profile":"default","purpose":"config"}
```

The canonical metadata shown above determines each volume identity and its
persisted metadata. An image path may initialize `_data` only when a tool
volume is first published. Later launches reuse the published volume.

Different applications using the same environment name and profile share one
home, and one application can use that home with different project sets.
Changing the profile selects a different home even when the environment name
is unchanged. Tool volume sharing is independent after applying any per-tool
profile override.

Configured XDG parent paths use ordinary host path resolution, including
symbolic links and existing directory modes. Toby leaves those parents and
symlinks unchanged, creates missing parents with the process umask, and lets
kernel access checks decide whether they are usable. At the final Toby-owned
`toby` boundary it follows any configured symlink, requires the resolved
directory to be accessible, pins it with a descriptor, and best-effort assigns
it to the effective user and group with mode `0700`. Failed repairs emit a
debug diagnostic and continue; actual filesystem operations determine whether
access is possible. Beneath that root, descriptor-rooted operations validate
Toby-owned volume child shapes, containment, and inode identity. Each volume
object directory receives the same best-effort repair, while its `_data`
directory retains the ownership and mode selected by the sandboxed application.
Exact duplicate requests coalesce; conflicting keys or overlapping sandbox
targets fail before mutation.

On first use, a home or tool volume may request seed contents from a path in
the immutable image. Toby publishes the complete metadata and `_data`
directory atomically. A failed or concurrent seed never exposes a partial
volume.

`toby volume create` derives the canonical metadata hash and atomically
publishes an empty `_data` directory. Repeating the same complete
type/name/profile/purpose specification returns the existing ID without
replacing its data. This exposes a safe destination for migrations before the
volume is selected by a launch.

`toby volume list` enumerates published hash IDs and validates each object's
canonical metadata and `_data` directory independently. It keeps malformed
objects visible as invalid entries so an operator can inspect or remove them.
Nonempty type, name, profile, and purpose filters match their corresponding
metadata fields exactly. Terminal output uses the Bubble Tea table component;
redirected output retains the stable plain table.

`toby volume inspect` reports the kernel-resolved native paths as YAML by
default and accepts `-o json` or `--output json` for JSON. `toby volume path`
prints only the resolved `_data` path for scripting. Both commands accept a
full ID, an unambiguous prefix of at least 12 lowercase hexadecimal characters,
or a complete metadata specification. An omitted exact-specification profile
normalizes to `default`.

`toby volume remove` accepts one or more IDs or a metadata filter and removes
the snapshot of all matching volumes. Without `--force`, a terminal-only
Bubble Tea confirmation shows every match before mutation. Terminal deletion
uses an inline spinner for the current volume and retains one completed row per
removed volume; redirected deletion emits only full volume IDs. Removal
remains descriptor-relative and no-follow without crossing a mount boundary.

Opening a volume retains a shared advisory lifecycle lease on its `_data`
directory. Shared leases permit concurrent applications and do not serialize
private-home or tool-volume use. Removal requests a non-blocking exclusive
lease, so a live launch causes removal to fail instead of unlinking its backing
directory.
See [management.md](management.md#volumes) for the complete volume command
surface.

The Docker tool is the sole storage exception. It does not request a Toby
volume: it binds the host user's `~/.docker` read-only and exposes the host
Docker socket only through its run-owned socket relay.

## Bubblewrap plan and descriptor discipline

`internal/sandbox/bwrap` separates a pure `Plan` from authoritative `Sources`.
The plan records sandbox-visible metadata; sources are already-opened
descriptors for the exact rootfs, home, projects, mounts, overlay directories,
runtime assets, and executable. Sources also retain non-mounted guard
descriptors for the complete OCI image store, Toby persistent-data root,
Bubblewrap run-storage root, and Toby runtime root.

Rendering validates:

- deterministic ordering and unique targets;
- no overlapping protected paths;
- exact source cardinality;
- device/inode/type identity for retained sources;
- descriptor ancestry proving that selected rootfs, home, managed, and overlay
  directories belong to their authoritative roots;
- rejection of a project that equals, contains, or is contained by any
  protected root, including through a symbolic-link target or changed
  diagnostic path;
- external-bind sources resolved through ordinary filesystem symlinks while
  rejecting magic-link traversal, then pinned as exact parent and
  basename-child capabilities, with directory-source ancestry (or
  non-directory parent ancestry) kept disjoint from every protected root and
  Toby-owned backing;
- environment names and bounded command input; and
- the selected namespace, capability, terminal, and network policy.

Bubblewrap consumes `/proc/self/fd/<n>` references rather than reopening
security-sensitive host paths. Background sidecar commands and environment
values are carried in a bounded sealed descriptor through Bubblewrap's `--args`
support, keeping secret values out of observable process arguments.

The executor selects among noninteractive, direct-terminal, and managed-PTY
modes. Noninteractive and background children start in a new session so they
cannot inherit a controlling terminal. A terminal supplied as stdin is replaced
with `/dev/null` for those commands, while explicit file or pipe input remains
connected.

The executor never classifies Bubblewrap errors from diagnostic text.
Bubblewrap's `--json-status-fd` provides process provenance: an exit event
identifies a payload exit, while a process exit before that event identifies a
pre-payload setup failure. For foreground commands, `--block-fd` keeps a
successfully configured sandbox init alive during the handoff. The child event
identifies both the init process and its mount-namespace inode. Toby retains the
exact process with a pidfd, opens its mount namespace, verifies both identities,
and only then releases the launch gate. After the Bubblewrap monitor returns,
Toby waits for the init to exit and closes the namespace descriptor before
another command reuses the run overlay.

Linux can defer releasing the OverlayFS superblock after that final namespace
reference is closed. A later command in the same run is therefore eligible for
a bounded retry only when Bubblewrap's structured status proves that the
payload did not execute. Toby launches these attempts through the trusted
`tobys exec` shim. The shim restores a separately inherited
stderr descriptor, opens a pidfd for its exact process, passes that pidfd to the
launch client over a dedicated Unix socket, signals payload readiness through a
dedicated pipe when retry provenance is required, and immediately calls `exec`.
The pidfd continues to identify the application after `exec`; client-mediated
graceful signals target it without terminating the Bubblewrap monitor. Output
is buffered verbatim up to a fixed bound before the readiness marker. A
successful marker flushes it and permanently switches to passthrough; a
replay-safe failed attempt discards it; buffer overflow makes the attempt
non-retryable. Payload output is never retried, including an exec failure or
exit status `1`. If Bubblewrap is interrupted after the ready marker but before
it writes a JSON exit event, Toby propagates the process status without
misclassifying the completed handoff as a pre-payload failure. If the bounded
window expires, Toby emits the final Bubblewrap diagnostic unchanged and
reports the setup failure.

Immediately after each foreground Bubblewrap child starts, and before any
goroutine can reap it, the executor opens a pidfd and proves that the exact
child is its process-group leader. The group pidfd remains open through
terminal job control, forced tree teardown, and `Wait`. Group signals use only
`pidfd_send_signal` with `PIDFD_SIGNAL_PROCESS_GROUP`; stopped and continued
states are observed with `waitid(P_PIDFD)`. The separately received payload
pidfd controls graceful application signals. If group identity retention fails,
Toby kills and reaps the still-unreaped exact child by its positive PID and
refuses the run. It never falls back to a negative numeric signal for a
Bubblewrap child group.

Managed-PTY mode is used only when it is enabled and stdin, stdout, and stderr
are the same terminal. It preserves terminal job-control semantics while
allowing Toby to display approval prompts. If terminal stdin is present but an
output stream is redirected, or managed-terminal mode is disabled, Toby uses
direct-terminal mode and preserves the three streams independently. With
managed mode disabled, operations not explicitly allowed cannot prompt and are
denied.

## Generated files

Selected tools contribute complete files through `internal/toolfiles`. Toby
validates the combined set before writing anything, resolves each target to the
private home or an owned tool volume, and atomically replaces the file.

Files are written at the applications' native paths. Concurrent launches
sharing the same home or tool volume use last-launch-wins behavior for these
Toby-owned files.

Static installers, wrappers, and other substantial assets are embedded
separately and mounted as transient runtime assets; they are not generated
configuration.

## Agent protocol and leases

The exact RPC fields, message ordering, byte-stream half-close behavior,
resource-log records, host-action payloads, and private helper contracts are in
[protocols.md](protocols.md).

Toby normally creates its agent directory with mode `0700` and its socket and
election lock with mode `0600`, subject to filesystem support. It can instead
adopt one systemd-owned descriptor. Filesystem access to the socket is the
transport boundary, with the kernel applying the endpoint's existing ownership
and mode. The agent serves the generated
`toby.agent.v1.AgentService` gRPC API over that private Unix socket. The
authored schema is
`proto/toby/agent/v1/agent.proto`; generated Go files use package
`agentv1` beneath `internal/gen/toby/agent/v1` and are not committed.
Request and response messages are distinct protobuf types. Closed concepts such
as resource kind, agent state, error code, output stream, and OCI phase are
protobuf enums rather than stringly typed message tags.

The client initiates every RPC. It first invokes `Hello` with a correlation ID
and its informational binary version. `HelloResponse` echoes the correlation ID
and reports the agent binary version, application protocol version, and
canonical resource-hash algorithm. This response can grow to carry more agent
information without adding an unsolicited bootstrap message.

The agent does not validate or select behavior from the client binary version,
and it does not decide whether the client is compatible. The client compares
the advertised protocol version with the versions it implements before opening
a session. A protocol mismatch stops the connection; binary build versions do
not.

`OpenSession` is a client-opened bidirectional stream. The agent returns a
fresh opaque session UUID and the supported transport capabilities in its
session-open response. The agent records every acquired lease against that
session. EOF, cancellation, or a transport error immediately invalidates the
session, revokes its host-action authority, cancels its in-flight operations,
and releases all of its leases. Host actions travel on this already
client-opened stream in response to activity that the launch forwarded from its
sandbox capability. The agent-stopping request is the other
agent-originated request and carries the launch's cleanup deadline.
Cancellation or failure of an independent RPC ends only that operation; it
does not revoke the agent session or its other streams.

The default agent invocation stops after its final agent session, resource
lease, retained resource runtime, and stream are gone.
It allows an initial connection grace period so a detached launcher can publish
the socket before its client connects. Warm-idle resources and in-progress OCI
operations defer shutdown until they finish. `tobyd --persistent`
disables idle auto-stop for an external service unit.

Hello returns the agent binary version. It is informational: when it
differs from the client binary version, the client emits the suppressible
`agent.binary-version-mismatch` warning with both versions and continues
after accepting the advertised protocol version. Resource acquire/release,
status, stop, resource listing, log following, model discovery, OCI preparation,
and method-specific MCP or models byte streams use their typed RPCs. gRPC
multiplexes those logical streams over the connection. Client-to-agent
half-close uses the generated stream's `CloseSend`; the application protocol
has no generic tunnel frame or end-write message.

The protobuf API is independent of its Unix transport. Toby advertises the
Unix-socket transport capability and serves no network listener.

Resource acquisition accepts exactly one input-only configuration document and
never accepts a caller-supplied resource ID or hash. The agent independently
applies defaults, normalizes and validates the typed configuration, serializes
an identity-schema `1` document canonically, and hashes it with BLAKE2b-512.
The complete 512-bit digest indexes the agent's stable resource entry. The
agent derives the external resource ID deterministically from the first 128
digest bits, overwriting the UUID version and variant fields to produce an RFC
9562 UUIDv8 with 122 digest bits. It retains the full digest to detect the
otherwise-unlikely external UUID collision. The client receives the UUID only
as an opaque resource ID alongside a new UUIDv4 lease ID.

Configuration is input-only: responses, status, resource lists, and logs never
contain it. A launch generates its own UUID for each sandbox-visible resource
and keeps this translation locally:

```text
client resource UUID -> resource kind -> resource ID -> lease ID
```

The agent validates both opaque IDs whenever a resource stream opens. Closing
the agent session releases all of its leases. Explicit release affects only
the named lease, so two launches may share one agent resource without
affecting each other.

Long operations use server-streaming RPCs and direction-specific accepted, item
or output, and terminal response types. Every response echoes the request's
UUID correlation ID. OCI preparation streams send complete absolute progress
snapshots for resolving, downloading, and extracting. The agent writes
progress and diagnostic records beneath
`$XDG_CACHE_HOME/toby/logs/<resource-kind>/<resource-id>/` before sending them.
A late client receives the latest snapshot and follows subsequent records from
the same operation. Nonterminal records are bounded on disk; the latest
snapshot remains available in memory, and a terminal record is always written.
Creating, appending, syncing, retaining, or closing an optional resource log
cannot fail the resource operation; those failures emit debug diagnostics and
the live protocol continues without persisted history.

MCP and models acquisition registers interest but leaves the runtime dormant.
First use coalesces concurrent startup. Unexpected failure closes affected raw
streams and subsequent use starts a replacement with jittered exponential
backoff from 250 milliseconds to at most five minutes. Intentional idle
teardown, final release, and agent shutdown do not restart a resource.

Resource and status snapshots include both lease-registered resources and
service-owned runtimes that remain active during warm idle or an in-progress
OCI operation. A retained runtime with no leases is reported with a lease count
of zero; neither snapshot includes configuration.

## Sandbox resource gateway

The launch client creates one random `0600` Unix socket, pins that exact socket
generation, and mounts it at `/run/toby/sandbox.sock`. It serves the
`toby.sandbox.v1` gRPC API from
`proto/toby/sandbox/v1/sandbox.proto`. The generated Go package is
`sandboxv1` beneath `internal/gen/toby/sandbox/v1` and is not committed. The
agent socket is not mounted.

`Hello` is a client-initiated unary RPC. The request carries a correlation ID
and an informational client binary version. The response echoes the correlation
ID and reports the serving Toby binary version and sandbox application-protocol
version. The client decides whether it supports that version before opening a
resource stream; the current version is `1`. The endpoint never sends an
unsolicited message.

`ConnectResource` is a bidirectional stream. Its first request contains a
correlation ID and one per-launch client resource UUID. A readiness response
confirms that the launch translated the UUID and opened the corresponding
agent resource stream. Later request and response messages carry bounded byte
chunks, and the generated stream's client half-close represents end of input.
MCP and models resource UUIDs share this registry; resource kind is launch-owned
metadata and is never supplied by the sandbox.

The socket service is transport-specific only at its outer gRPC listener.
Neither its protobuf API nor its resource-opening contract depends on an MCP
wire protocol. Revoking the endpoint cancels all active resource streams.

## MCP gateway

Every application-facing MCP entry is a stdio command:

```text
/toby/bin/tobys resource connect -- <client-resource-uuid>
```

The adapter selects the client resource UUID through the sandbox gRPC service.
The launch translates it through the active lease, opens a `ConnectMCP` agent
stream, and relays MCP bytes without interpreting them. Configured names,
resource IDs, lease IDs, URLs, headers, commands, environment values, and
credentials never enter the sandbox.

Backend behavior is transport-specific:

- built-in Toby: a fresh server session per connector, with host Git calls
  reversed through the agent session to the launch process;
- local stdio: a fresh Bubblewrap sidecar process per connector;
- local HTTP: an optional shared Bubblewrap sidecar generation with one
  independent Streamable HTTP session per connector; and
- remote HTTP: one independent upstream Streamable HTTP session per connector.

Local HTTP endpoints are Unix sockets directly beneath `/run/toby` inside the
sidecar sandbox. The bridge enforces request, response, header, body,
concurrency, and session limits. Upstream endpoints and credentials remain
host-side.

The built-in server exposes Git operations and bounded `toby://` resources
created from an immutable session snapshot. Snapshot views never contain host
paths, capability paths, upstream URLs, headers, commands, environment values,
or credentials.

## Models gateway

Models-resource configuration remains host-side. For each configured resource:

1. the launch registers one independent agent resource and lease without
   starting Caddy;
2. the launch requests that resource's model list, which lazily installs the
   agent-private Caddy route and queries the configured models endpoint;
3. the launch creates a loopback route keyed by its per-launch client resource
   UUID and protected by a synthetic credential; and
4. tools receive only the launch loopback URL, synthetic credential, display
   metadata, and discovered model document.

Models-list requests use the same authorized route and cache a successful
result for five minutes. Concurrent requests for the same effective resource
share one discovery operation. Cache flush targets one acquired models
resource.

Caddy runs from the official `docker.io/library/caddy:latest` OCI image under
Bubblewrap with a read-only filesystem, host networking, and no private home
or projects. The image is pulled only when missing. Separate owner-only Unix
sockets carry administration, models data, and authorization. None is
mounted into an application sandbox.

During Bubblewrap mount assembly, an anonymous temporary overlay permits
creation of bind targets that an OCI image omits, such as `/etc/resolv.conf`.
After the exact DNS, authorization, and runtime mounts are installed,
Bubblewrap remounts `/` read-only before Caddy executes. The anonymous upper is
never persisted. Purpose-specific child mounts retain their separately defined
access, including ephemeral `/tmp`, `/run`, and `/dev` plus the owner-only
service runtime.

The launch loopback relay requires the expected synthetic credential. It
strips that credential before opening an agent stream. The agent-private Caddy
route installs fixed host-owned headers and contacts the upstream. Revocation
denies new requests synchronously; an already accepted stream may drain.

Model discovery uses the same agent-private route reached by application
streams. Configuration updates are complete snapshots; a rejected load does
not replace the last accepted generation.

## Security boundaries

The major trust boundaries are:

- host configuration may contain secrets and is never copied wholesale into a
  sandbox;
- the launch process has temporary host Git authority, but the agent does not;
- the agent socket is host-only;
- the sandbox resource capability exposes only per-launch client resource
  UUIDs backed by active agent leases;
- models applications receive synthetic credentials, never real headers;
- persistent paths are opened and validated before Bubblewrap consumes them;
- direct project selection is constrained beneath `XDG_PROJECTS_DIR`, while a
  launch file must explicitly name any additional project source; and
- generated tool files are restricted to validated owned backings.

Host networking is a known limitation. It does not replace capability
authentication: sandbox resource streams use an owner-only Unix socket, and
models HTTP requests require a launch-owned loopback route plus a synthetic
credential.

A process deliberately sharing a private home or tool-volume profile can read
the current generated models route and synthetic credential because generated
files use last-launch-wins behavior. It can exercise that active models
capability, but cannot recover the real upstream credential or access
agent or Caddy administration sockets.

The built-in `docker` tool is an explicit exception to normal isolation.
Selecting it creates a run-scoped Unix relay beneath the exact run runtime
directory and binds only a pinned descriptor for that endpoint at
`/run/toby/docker.sock`. The invoking launch process opens upstream Docker
connections with its retained host credentials and supplementary groups.
An unprivileged user namespace maps only the primary GID; Linux necessarily
retains existing supplementary kernel GIDs, which appear as the overflow GID
inside the namespace and remain effective only against host objects Toby
explicitly binds. The real host socket is never mounted, so possessing its
group in the sandbox does not reveal a path to it. The relay socket is mode
`0600`, is reachable only through its run-scoped bind, and is revoked with the
run.
Read-only Docker client configuration is the only additional bind. No runtime,
agent, OCI, MCP, or models component otherwise depends on Docker.

## Package map

| Area | Packages |
| --- | --- |
| Commands | `cmd/toby`, `cmd/tobyd`, `cmd/tobys` |
| Composition and CLI | `internal/app/client`, `internal/app/agent`, `internal/app/sandbox`, `internal/cli`, `internal/version` |
| Host/launch config | `internal/config`, `internal/config/file`, `internal/config/session`, `internal/config/app`, `internal/config/launch`, `internal/config/mcp`, `internal/config/mcpresource`, `internal/config/models`, `internal/config/ociresource` |
| Tool contract and implementations | `internal/tools`, `internal/tools/helpers`, `internal/tools/kit`, `internal/tools/builtin/*`, `internal/tools/fake`, `internal/tools/runtimepath`, `internal/tools/wiring` |
| Lifecycle and generated files | `internal/lifecycle`, `internal/toolfiles`, `internal/runtimeassets` |
| Native sandbox | `internal/sandbox`, `internal/sandbox/layout`, `internal/sandbox/mount`, `internal/sandbox/bwrap` |
| OCI and storage | `internal/oci` and its subpackages, `internal/storage` and its subpackages |
| Agent | `internal/agent` and its `client`, `clientresource`, `progressio`, `protocol`, `resource`, `resourcelease`, `resourcelog`, `resourceprogress`, `server`, and `socket` subpackages; `proto/toby/agent/v1` |
| Sandbox resource gateway | `internal/sandboxgateway`, `internal/sandboxgateway/protocol`, `proto/toby/sandbox/v1` |
| MCP and host actions | `internal/mcpgateway` and subpackages, `internal/tobymcp`, `internal/hostaction`, `internal/hostaction/methods/git` |
| Models | `internal/providers` and its protocol adapters, `internal/providergateway` and its subpackages |
| Policy/presentation | `internal/approval`, `internal/diagnostic`, `internal/diagnostic/exitcode`, `internal/diagnostic/warning`, `internal/permission`, `internal/shutdown`, `internal/status` |
| Session orchestration | `internal/session/run`, `internal/socketrelay` |
| Shared implementation support | `internal/executable`, `internal/resourcehash`, `internal/uuid` |
