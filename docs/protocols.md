# Protocols

Toby has two versioned gRPC application protocols and several private helper
contracts:

- `toby.agent.v1` connects a host launch CLI to the per-user agent;
- `toby.sandbox.v1` connects a process inside one sandbox to the launch CLI;
- JSON-RPC host-action payloads cross the agent session as opaque protobuf
  bytes; and
- descriptor-based helper protocols coordinate the host Git supervisor and
  Bubblewrap payload handoff.

The authored protobuf schemas are:

- [`proto/toby/agent/v1/agent.proto`](../proto/toby/agent/v1/agent.proto)
- [`proto/toby/sandbox/v1/sandbox.proto`](../proto/toby/sandbox/v1/sandbox.proto)

Generated Go files live beneath `internal/gen/toby/{agent,sandbox}/v1` and
are not committed. Run `make gen/grpc` to regenerate them.

## Versioning and message dispatch

Both application protocols advertise version `1`. The protobuf
package suffix is also `v1`, but application compatibility is selected from
the numeric `protocol_version` returned by each `Hello` RPC. The serving side
reports its version; the connecting client decides whether it implements that
version. The informational binary version does not participate in protocol
selection.

A client always sends the first message:

- an agent client invokes `Hello`, then opens `OpenSession`; and
- a sandbox process invokes `Hello`, then opens `ConnectResource`.

The server sends no bootstrap message. A server-streaming RPC may return
multiple events for one client request. The agent can issue two
requests on a client-opened `OpenSession` stream:

- a host-action request caused by an action that originated through that
  launch's sandbox capability; and
- one `ShutdownRequest` when process shutdown begins.

The shutdown request is the lifecycle exception to the
client-initiated-message rule. It gives the launch CLI time to stop its
foreground sandbox and release launch-owned capabilities before the agent
session disappears.

gRPC does not need a request-type field. The HTTP/2 gRPC method path selects an
RPC descriptor, and that descriptor names the concrete protobuf request and
response types. Within a stream that supports more than one message shape, a
protobuf `oneof` field tag selects the concrete shape. Closed value sets such
as resource kind, agent state, output stream, and OCI phase use protobuf
enums.

Pre-1.0 protocol changes replace version-1 behavior directly. Clients must
still treat unknown enum values, fields, and opaque identifiers according to
normal protobuf and gRPC compatibility rules rather than inferring meaning
from their current representation.

## Common conventions

Every request has a nonempty `correlation_id`, and every response or stream
event caused by it echoes that value. Toby clients generate UUIDv4 values, but
servers validate correlation IDs only as bounded opaque strings. A correlation
ID may not be reused while another request with that ID is active in the same
agent session. For a bidirectional byte stream, every message keeps the
correlation ID from its opening message.

The current bounds are:

| Value | Limit |
| --- | ---: |
| Encoded gRPC message | 1 MiB |
| One MCP or models byte chunk | 32 KiB |
| Resource configuration JSON | 256 KiB |
| Correlation, session, resource, lease, or operation ID | 256 bytes |
| Informational binary version | 128 bytes |
| Protocol error message | 4 KiB |
| Sandbox resource registry | 4,096 entries |
| Concurrent sandbox resource streams | 128 by default, 256 maximum |
| Concurrent agent RPCs | 256 by default |
| Agent client-drain deadline | 20 seconds actual, 17 seconds advertised |
| Agent gRPC drain after client cleanup | 5 seconds |
| Agent resource teardown | 10 seconds |
| Fx process finalization | 5 seconds |
| Sandbox SIGTERM and SIGKILL reap | 2 seconds each |

Resource configuration and host-action payloads must be one JSON object.
Duplicate object fields are rejected. Configuration is input-only: the agent
never returns it through acquisition, status, listing, logs, or error details.

Closing a gRPC send side represents end of input. There is no generic tunnel
frame or explicit end-write message. The relay calls `CloseWrite` on the
underlying byte stream when it supports half-close, while its output direction
may continue until EOF. Stream completion or cancellation closes the
corresponding resource stream without closing unrelated RPCs or the agent
session.

## Agent transport and filesystem access

The launch normally reaches the agent at:

```text
$XDG_RUNTIME_DIR/toby/agent.sock
```

An optional system-wide systemd socket instead listens at:

```text
/run/toby/users/<username>/toby/agent.sock
```

It is gRPC over a Unix stream socket. Access is delegated to ordinary
filesystem permissions: Toby-created agent sockets and election locks request
mode `0600`, and their Toby-owned runtime directory requests mode `0700`. The
system-wide socket unit creates a mode-`0600` socket owned by its selected user.
Corrective ownership or mode failures emit debug diagnostics and continue; an
operation fails only when an ordinary kernel access check prevents it.

The socket is host-only. It is never mounted into an application or sidecar
sandbox. A client uses the normal runtime endpoint when it exists, falls back
to an existing system-wide endpoint, and otherwise uses the normal endpoint
with agent autostart. An endpoint's agent elects or adopts only that endpoint;
the unused socket does not block startup. The agent advertises the Unix-socket
transport capability, while the protobuf service remains independent of a Unix
path.

## `toby.agent.v1.AgentService`

The service exposes these RPCs:

| RPC | Shape | Purpose |
| --- | --- | --- |
| `Hello` | unary | Report agent binary version, protocol version, and hash algorithm |
| `OpenSession` | bidirectional stream | Own one client connection's leases and reverse host actions |
| `AcquireResource` | unary | Register one OCI, MCP, or models configuration |
| `ReleaseResource` | unary | Release one lease owned by the session |
| `Status` | unary | Return non-secret agent state and activity counts |
| `Stop` | unary | Acknowledge and begin graceful agent shutdown |
| `ListResources` | server stream | Return non-secret active resource entries |
| `ReadResourceLog` | server stream | Return one retained JSONL operation log |
| `ListModels` | server stream | Discover or return cached models for one lease |
| `FlushModelsCache` | unary | Invalidate one models resource's discovery cache |
| `PrepareOCI` | server stream | Start or join one image preparation operation |
| `ConnectMCP` | bidirectional stream | Open one lease-authorized MCP byte stream |
| `ConnectModels` | bidirectional stream | Open one lease-authorized raw HTTP byte stream |

### Hello

`HelloRequest` contains:

- `correlation_id`;
- `binary_version`, the client's informational build version.

`HelloResponse` echoes the correlation ID and contains:

- `binary_version`, the agent build version;
- `protocol_version`, `1`;
- `hash_algorithm`, `blake2b-512`.

The agent does not decide whether the client version is compatible and does
not use the client binary version for behavior. The client rejects an
unsupported protocol version. A binary-version difference emits
`agent.binary-version-mismatch` with both versions and otherwise continues.

### Agent session

`OpenSession` is a client-opened bidirectional stream. Its first client message
must be a `SessionOpenRequest`. The server answers with
`SessionOpenResponse`, containing:

- a fresh opaque UUIDv4 session ID; and
- the supported transport capabilities.

Later client messages are `HostActionResponse`, `HostActionError`, or
`ShutdownResponse`. Later server messages are `HostActionRequest`,
`HostActionCancel`, or `ShutdownRequest`. Each agent request has an
agent-generated correlation ID, and its client response uses the same ID.

`ShutdownRequest` carries `grace_period_milliseconds`. The production
agent allows 20 seconds but advertises 17 seconds, leaving a three-second
server-side margin. The client uses that advertised value as its controlling
cleanup deadline. It sends `SIGINT` to a foreground sandbox and allows 12
seconds for graceful exit by signaling the exact payload pidfd while Bubblewrap
continues monitoring the sandbox. The remaining five seconds cover process-group
`SIGTERM`, `SIGKILL`, bounded reap, and local scheduling before the client
releases launch-owned resources and leases, sends `ShutdownResponse`, and
closes the session. Disconnecting is equivalent to acknowledgement. The agent
invalidates sessions that neither acknowledge nor disconnect before its private
deadline.

The stream is the lifetime authority for its session. EOF, cancellation, or a
transport failure:

- makes the session unavailable to new requests;
- revokes the launch's host-action caller;
- releases every lease acquired through the session.

Closing the complete gRPC connection also cancels its active RPC contexts. An
explicit RPC failure does not by itself close the session or unrelated streams.

### Resource acquisition and identity

`AcquireResource` sends:

- the session ID;
- one `ResourceKind`: `OCI`, `MCP`, or `MODELS`;
- one resolved JSON configuration document.

It does not send a resource hash or a proposed resource ID. The agent strictly
decodes the resource-specific configuration, rejects unknown fields, applies
defaults, and canonicalizes this identity document:

```json
{
  "schema": 1,
  "kind": "resource.oci | resource.mcp | resource.models",
  "configuration": {}
}
```

The agent hashes the canonical JSON with BLAKE2b-512. The complete 512-bit
digest indexes the resource registry and is retained for collision detection.
The external resource ID is a deterministic RFC 9562 UUIDv8 derived from the
first 128 digest bits; setting the UUID version and variant leaves 122 digest
bits. Clients treat this ID as opaque and do not derive or parse it.

`ResourceAcquireResponse` returns:

- the stable opaque resource ID; and
- a fresh independently releasable UUIDv4 lease ID.

Equivalent normalized configurations share one resource ID but receive
different leases. Registration does not start an MCP or models runtime.

`ReleaseResource` names one lease. Release is idempotent after that same
session has already released the lease. A missing lease or one owned by another
session returns `LEASE_NOT_FOUND`. Session teardown releases all remaining
leases with a bounded cleanup context.

The launch separately generates a UUIDv4 client resource ID for each
sandbox-visible registration. Its local translation is:

```text
client resource ID -> resource kind -> agent resource ID -> lease ID
```

The client ID changes on every launch. It is never an agent identity and is
not sent by sandbox code to the agent.

### Status, stop, and resource listing

`Status` returns the agent binary version, state (`STARTING`, `READY`, or
`STOPPING`), and counts of active sessions, leases, resources, and streams.
Resource counts include both lease-registered entries and service-owned
runtimes retained during startup or warm idle.

`Stop` acknowledges the unary request and transitions the agent toward
graceful shutdown. Every connected session then receives the
`ShutdownRequest` described above. After the client-drain deadline, the
agent cancels remaining sessions, allows gRPC five seconds to drain, and
force-stops the transport if necessary. Agent-owned resource teardown has a
separate ten-second bound. When systemd owns the listening socket, that socket
remains available and can activate a new agent later.

`ListResources` returns ordered `ResourceListItem` messages containing a
positive sequence, opaque resource ID, resource kind, and active lease count.
Normal stream EOF is completion; this RPC has no separate terminal message.
Configuration is never included.

### Resource logs

`ReadResourceLog` names a resource kind and ID. An optional operation ID
selects one exact retained log; an empty value selects the newest log.

The stream sends:

1. `ResourceLogAccepted` with the selected operation ID;
2. one or more ordered `ResourceLogChunk` messages containing exact raw file
   bytes; and
3. `ResourceLogComplete`, or `ResourceLogFailed`.

Chunks are not parsed protocol events. They are portions of a JSON Lines file,
so a JSON record may cross chunk boundaries. The CLI writes the bytes directly
to its selected output.

Logs live at:

```text
$XDG_CACHE_HOME/toby/logs/<resource-kind>/<resource-id>/<operation-id>.jsonl
```

Toby retains the newest 32 logs per resource. Each log is requested with mode
`0600`. Nonterminal data is bounded at 64 MiB, while a terminal record is
written even after truncation.

Resource logs are optional persistence. Failure to create, append, sync,
retain, or close a log emits a debug diagnostic and does not fail the OCI,
MCP, or models operation. `ReadResourceLog` still returns an operation error
when the requested retained log cannot be opened or streamed.

OCI log records contain:

```json
{
  "operation_id": "opaque",
  "sequence": 1,
  "kind": "progress | output | complete | failed",
  "source": "registry | extractor | cache",
  "stream": "stdout | stderr",
  "progress": {
    "phase": "resolving | downloading | extracting",
    "completed_bytes": 0,
    "total_bytes": 0,
    "completed_items": 0,
    "total_items": 0
  },
  "data": "base64",
  "cached": true
}
```

Fields not applicable to a record kind are omitted. Progress values are
absolute snapshots, not deltas. JSON encodes raw `data` bytes as base64.

MCP and models generation logs contain:

```json
{
  "generation_id": "opaque",
  "sequence": 1,
  "kind": "progress | truncated | complete | failed",
  "progress": {
    "sequence": 1,
    "operation": "opaque",
    "kind": "step | output | complete | failure",
    "source": "component",
    "text": "status",
    "stream": "stdout | stderr",
    "data": "base64"
  }
}
```

The `truncated` record means later nonterminal records exceeded the per-log
bound. Configuration, headers, environment values, commands, and upstream URLs
are not added to log metadata.

### Models operations

`ListModels` names a models lease owned by the session. It sends:

1. `ModelsListAccepted` with a new opaque operation ID;
2. `ModelsListItem` messages sorted by model ID, each with a positive sequence,
   model ID, and JSON-encoded model document; and
3. `ModelsListComplete`, or `ModelsListFailed`.

Successful discovery is cached for five minutes by effective models resource.
Concurrent requests share one discovery flight. `FlushModelsCache` invalidates
only the cache associated with the named active models lease.

### OCI preparation

`PrepareOCI` names an OCI resource ID and its lease. It starts a preparation
operation or attaches to matching work already owned by the agent. The stream
uses this order:

1. `OCIPrepareAccepted` with an opaque operation ID;
2. optionally, `OCIPrepareSnapshot` with the latest absolute progress when the
   client attaches after work has begun;
3. ordered `OCIPrepareProgress` or `OCIPrepareOutput` events; and
4. `OCIPrepareComplete`, or `OCIPrepareFailed`.

Progress covers resolving, downloading, and extracting. Each snapshot carries
completed and total byte and item counts. Output identifies its registry,
extractor, or cache source and stdout or stderr provenance. A complete event's
`cached` field is true when no preparation work was needed. Presentation
clients intentionally do not create a transient status row for an immediate
cached completion.

The operation record is written before followers are notified. A later client
can therefore attach to the current snapshot and then follow the same
disk-backed operation without relying on an agent-wide in-memory transcript.

### MCP and models byte streams

`ConnectMCP` and `ConnectModels` are separate RPCs so the resource kind is
selected by the method rather than by a caller-supplied enum.

The first request contains:

- the correlation ID;
- the session ID;
- the agent resource ID;
- the lease ID.

The agent validates that all three IDs describe the same active lease, opens
the backend, rechecks the lease, and sends `ready`. Subsequent messages in
either direction contain only byte chunks. A repeated open message or changed
correlation ID is invalid.

`ConnectMCP` carries one MCP protocol session. `ConnectModels` carries raw
HTTP/1.x bytes between the launch's loopback reverse proxy and the
agent-private Caddy route; it is not a Toby-specific JSON models API.

Unexpected backend failure closes affected streams. The next use starts or
joins a replacement generation with jittered exponential backoff, capped at
five minutes by the MCP and models handlers. Intentional idle teardown, final
release, and agent shutdown do not trigger restart.

### Agent errors

Failures before a streaming operation is accepted use gRPC status with a
`toby.agent.v1.ErrorDetail` containing the correlation ID, closed error code,
safe message, and `retryable` hint.

| Error code | gRPC status |
| --- | --- |
| `INVALID_REQUEST` | `InvalidArgument` |
| `ACQUIRE_FAILED` | `Unavailable` |
| `LEASE_NOT_FOUND` | `NotFound` |
| `UNAVAILABLE` | `Unavailable` |
| `INTERNAL` | `Internal` |

After a long operation is accepted, its stream uses the method-specific
`failed` terminal event when possible. Error messages are deliberately
non-secret and do not echo resource configuration.

## `toby.sandbox.v1.SandboxService`

The launch serves this API on an exact run-owned Unix socket mounted at:

```text
/run/toby/sandbox.sock
```

The endpoint is requested with mode `0600` and pins one socket generation.
Access is controlled by that run-scoped filesystem capability. Closing the
launch endpoint revokes its registry and cancels every active stream. The
agent socket is not present in the sandbox.

The service exposes:

| RPC | Shape | Purpose |
| --- | --- | --- |
| `Hello` | unary | Report launch binary version and sandbox protocol version |
| `ConnectResource` | bidirectional stream | Open one launch-registered byte capability |

`HelloRequest` contains a correlation ID and informational client binary
version. `HelloResponse` echoes the correlation ID and reports the launch
binary version and sandbox protocol version `1`. The sandbox client
decides compatibility.

The first `ConnectResource` request contains a correlation ID and one
launch-generated client resource ID. The launch looks it up in its immutable
run registry and calls the associated opener. An unknown ID returns
`NotFound`, an opener failure returns `Unavailable`, and exhausting the
connection limit returns `ResourceExhausted`. On success, the service sends
`ready`; later messages carry only byte chunks with the original correlation
ID.

The sandbox supplies no resource kind, agent resource ID, lease ID,
configuration, URL, header, command, or credential. The launch-owned opener
contains that translation.
`tobys resource connect -- <client-resource-id>` first calls `Hello`, then
opens this stream and copies stdin/stdout bytes without interpreting MCP.

The registry can contain MCP and models openers. Tool MCP integrations use the
stdio connector. Models integrations normally use the HTTP capability
described below.

## Models HTTP capability

For every configured models resource, the launch creates:

- a fresh client resource UUID;
- a 256-bit random synthetic credential; and
- a path below one IPv4 loopback listener:

```text
http://127.0.0.1:<random-port>/<client-resource-id>
```

The remainder of the request path is preserved. For an OpenAI-compatible
resource, model discovery is therefore available at:

```text
http://127.0.0.1:<port>/<client-resource-id>/models
```

OpenAI-compatible routes require exactly:

```http
Authorization: Bearer <synthetic-credential>
```

Anthropic routes require exactly:

```http
X-Api-Key: <synthetic-credential>
```

Missing or incorrect credentials receive an empty `401 Unauthorized`
response. Before opening `ConnectModels`, the launch removes both
`Authorization` and `X-Api-Key`. The agent-private Caddy route authenticates
its own separate synthetic credential, strips client-controlled authentication
and forwarding headers, applies the real configured host headers, and
preserves the configured upstream base path.

Loopback is not the authorization boundary because another host process can
reach the port. The random route and credential form the revocable
launch-scoped capability. Neither reveals the upstream URL or headers.

## Host-action JSON-RPC

The built-in Toby MCP translates approved host Git tools into JSON-RPC 2.0
objects carried as the opaque `payload` bytes of `HostActionRequest` and
`HostActionResponse`. Protobuf supplies the outer framing, so the JSON has no
length prefix and a request does not depend on newline framing.

A request has:

```json
{
  "jsonrpc": "2.0",
  "id": 1,
  "method": "git.commit",
  "params": {}
}
```

The integer JSON-RPC ID is internal to the built-in MCP session and is distinct
from the outer protobuf correlation ID. Successful results have:

```json
{
  "jsonrpc": "2.0",
  "id": 1,
  "result": {
    "repository": "project-name",
    "exit_code": 0,
    "stdout": "",
    "stderr": ""
  }
}
```

The supported method parameters are:

| Method | Parameters |
| --- | --- |
| `git.commit` | `repository`, `message`, optional `amend` |
| `git.fetch` | `repository` |
| `git.push` | `repository`, `branch`, optional `origin`, optional `tags` |
| `git.rebase` | `repository` and exactly one of `base`, `continue`, or `abort` |
| `git.tag` | `repository`, `tag`, `message`, optional `target` |

Repository values name sandbox-visible projects, not host paths. The launch
resolves them against its immutable project snapshot and applies action
approval before starting Git.

JSON-RPC errors use the standard parse, invalid-request, method-not-found,
invalid-params, and internal codes, plus:

| Code | Meaning |
| ---: | --- |
| `-32007` | Project is not visible to the launch |
| `-32008` | Permission was denied |

The agent does not execute Git and has no standing Git credential authority.
The live launch process handles the request; losing the agent session revokes
that route.

## Private Git supervisor protocol

An approved Git operation re-executes the exact current `toby` binary through
`/proc/self/exe` with the private discriminator
`__toby_internal_git_supervisor_v1`. This is an internal process contract, not
a public CLI.

The child receives:

| Descriptor | Purpose |
| ---: | --- |
| `3` | Exact opened repository directory |
| `4` | Lifetime pipe; EOF means the launch owner died |
| `5` | Direct regular file for final status |
| `6` | Sealed regular file containing bounded arguments |

The argument document is:

```json
{"version":1,"command":["git","..."]}
```

It is limited to 1 MiB and 1,024 arguments, starts with `git`, contains no NUL
bytes, and is sealed against shrink, growth, and writes before exec.

An ordinary completed status document is at most 4 KiB:

```json
{
  "version": 1,
  "started": true,
  "exitCode": 0
}
```

On a supervisor failure, `failure` is `not_found`, `permission`, or `internal`
and `error` carries a bounded diagnostic. `canceled` is present and true when
the launch lifetime canceled the operation. The status exit code must match
the supervisor process status. The supervisor is a child subreaper and does
not report completion until the Git process and adopted descendants have been
terminated or reaped.

## Bubblewrap helper contracts

Toby consumes Bubblewrap's bounded newline-delimited JSON status stream from
`--json-status-fd`. It requires this ordered event pair:

1. a child event containing a positive `child-pid` and `mnt-namespace`; then
2. an exit event containing an `exit-code` from 0 through 255.

The complete status stream is limited to 16 KiB. A missing, duplicated,
out-of-order, malformed, or oversized event is a runtime error. Toby does not
identify Bubblewrap errors by matching stderr text.

For foreground commands, `--block-fd` holds the sandbox init after mount setup.
Toby uses the child event to retain the exact process with a pidfd, verifies
its parent and reported mount-namespace inode, opens that namespace, and only
then closes the block descriptor. After the Bubblewrap monitor returns, Toby
waits for the retained init to exit before releasing the namespace descriptor
and allowing the run overlay to be reused.

Only a later command reusing an already-used run overlay is eligible for the
bounded deferred-unmount retry. Toby inserts this trusted immediate exec shim:

```text
/toby/bin/tobys exec <ready-fd|-1> <stderr-fd|-1> <signal-fd|-1> -- <argv...>
```

The helper is recognized only when `TOBY_SANDBOX=1`. It restores an inherited
direct-terminal stderr descriptor when present. For foreground commands, it
opens a pidfd for its exact process and sends that descriptor with marker
`0x01` over the Unix `SOCK_SEQPACKET` capability at `signal-fd`. For
retry-authorized commands, it then writes `0x01` to `ready-fd`. It closes the
capability descriptors, resolves the command with `execvp`-like `PATH`
behavior, and immediately calls `exec`; the transferred pidfd continues to
identify the resulting application process. It exits `127` when no command is
found and `126` when the command cannot be invoked.

Before the ready byte, relevant attempt output is buffered verbatim up to
64 KiB. The marker commits the buffer and switches permanently to passthrough.
A structured pre-payload setup failure may discard the buffer and retry. EOF
without a marker, marker corruption, buffer overflow, or any evidence that the
payload started makes replay unavailable. The final failed attempt flushes its
unaltered output once.

These helper contracts separate provenance from diagnostic bytes: application
output can begin with any text, including `bwrap:`, without being delayed,
rewritten, or classified as Bubblewrap output.
