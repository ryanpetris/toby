# Protocols

Toby's processes talk over Unix sockets on the host and over Cloud
Hypervisor's hybrid vsock between host and guest. Every protocol is at
version 1.

## Frames

Every message is one frame:

```text
u32 big-endian length | u8 type | CBOR payload
```

The length counts the type byte and the payload. Frames are at most 64 KiB;
senders split larger data (terminal output, replay buffers) across frames.
Receivers ignore unknown fields in a payload and reject unknown frame types.
Each protocol has its own set of type numbers, listed below.

## vsock transport

Each machine has two hybrid vsock sockets in its runtime directory
(`/run/user/<uid>/toby/machines/<id>/` with the systemd user back end):

- `vsock.sock`: the host connects, writes `CONNECT <port>\n`, reads
  `OK <port>\n`, and the stream is then connected to the guest listener on
  that port.
- `vsock.sock_1024`: `toby internal machine` listens here for connections the
  guest opens to host port 1024.

`toby guest relay` listens on guest vsock port 1024 and accepts connections
only from the host (CID 2).

## Stream headers

The first frame of every vsock connection is a header. The receiver answers
with a reply frame; after an accepting reply the connection carries raw bytes
or the protocol the header names.

Headers the host sends to the relay:

| Type | Header | After an accepting reply |
| --- | --- | --- |
| 1 | `Control { proto_versions }` | relay control requests |
| 2 | `SessionAttach { session_id }` | the session protocol, end to end |
| 3 | `Dial { target }` | raw bytes to a guest endpoint (`tcp` address or `unix` path) |

Headers the relay sends to the host:

| Type | Header | Meaning |
| --- | --- | --- |
| 16 | `RelayHello { version, proto_versions, boot_id }` | the relay started |
| 17 | `Accepted { listener_id }` | a guest listener accepted a connection |

Any guest process can open these connections, so `toby internal machine`
treats a `RelayHello` only as a prompt to query the relay over its control
channel, and splices an `Accepted` connection only to a host target
registered for that listener (by a forward or a capability), refusing
unknown listeners.

Replies:

| Type | Reply |
| --- | --- |
| 112 | `Ok { version }` (the negotiated version, for headers that negotiate one) |
| 113 | `Refused { error }`; the connection closes |

The machine's `session.sock` accepts the same `SessionAttach` header from host
clients and passes it, the reply and the rest of the stream through to the
relay.

## Relay control

Requests are answered in order on the control channel.

| Type | Request | Response |
| --- | --- | --- |
| 1 | `Spawn { spec, version }` | `Spawned { session_id }` once the session's socket exists |
| 2 | `Listen { listener_id, bind, mode }` | `Done` |
| 3 | `Unlisten { listener_id }` | `Done` |
| 4 | `Sessions {}` | `SessionList { sessions }` |
| 5 | `Kill { session_id, signal }` | `Done`; the signal goes to the session's process group |
| 6 | `Forget { session_id }` | `Done`; discards an exited session's record |
| 7 | `Ping {}` | `Done` |
| 8 | `Hello {}` | `RelayInfo { version, boot_id }` |

Response types: 64 `Done`, 65 `Spawned`, 66 `SessionList`, 67
`Failed { error }`, 68 `RelayInfo`.

A spawn `spec` holds the session ID, `argv`, extra environment, working
directory, identity (`user` or `root`), terminal size (absent for a session
without a terminal), whether the exit is kept for a later client, and whether
the command starts only when the first client attaches (so none of its output
precedes a reader). Repeating a spawn with the same spec is harmless; a
different spec with an existing session ID fails. `version`
names the runtime version whose binary runs the session
(`/run/toby/fs/versions/<version>/toby`). The relay starts it with
`systemd-run --scope`, so sessions live in their own scope units and survive
relay restarts.

## Session protocol

Between a client and `toby guest session`, through the relay and
`toby internal machine`, which only splice.

Client frames:

| Type | Frame |
| --- | --- |
| 1 | `Hello { versions, rows, cols, want_replay, resume_from }`, the first frame |
| 2 | `Stdin { bytes }` |
| 3 | `CloseStdin {}` |
| 4 | `Resize { rows, cols }` |
| 5 | `Signal { signal }`, delivered to the session's process group |

Session frames:

| Type | Frame |
| --- | --- |
| 64 | `Welcome { version, state, tty, offset, lost, input, input_closed }`; `state` is `running` or the exit status; `input` counts standard input bytes the session has received and `input_closed` says whether it was closed |
| 65 | `Replay { bytes, stderr }`: recent output (up to 1 MiB) after the welcome, if requested; `stderr` marks standard-error output of sessions without a terminal |
| 66 | `Stdout { bytes }` |
| 67 | `Stderr { bytes }` (sessions without a terminal) |
| 68 | `Exit { status }` |
| 69 | `Detached { reason }`: another client attached; the connection closes |
| 70 | `Refused { error }` |

Output is numbered by offset: the count of output bytes since the session
started. `Welcome.offset` is the offset of the first byte the client receives
next. A client that reconnects sends `resume_from` with the offset it has
reached and receives the buffered output from there (the session keeps the
last 8 MiB for this); `Welcome.lost` counts bytes that were no longer buffered.
Without `resume_from`, `want_replay` selects the last 1 MiB or nothing.

A client of a session with a terminal that stops reading for 30 seconds is
disconnected; for a session without a terminal, output waits for the client
(other clients can still attach), and a session that starts on attach also
holds its output while no client is attached. Input is not resent after a reconnection; a
client compares `input` and `input_closed` with what it sent to tell whether
input was lost.

One client is attached at a time; a new attachment detaches the previous one.
A command that cannot be started (for a session that starts on attach) writes
the error to the client and exits with status 127.
When a session with a kept exit ends while no client is attached, it waits up
to an hour for a client to collect the exit.

## Machine control

`toby internal machine` answers on the machine's `control.sock`. The first
request is `Hello`; requests are answered in order.

| Type | Request | Response |
| --- | --- | --- |
| 1 | `Hello { versions }` | `Welcome { version, machine_id, toby_version }` |
| 2 | `Spawn { spec }` | `Spawned { session_id }` |
| 3 | `Sessions {}` | `SessionList { sessions }` |
| 4 | `Kill { session_id, signal }` | `Done` |
| 5 | `Status {}` | `MachineStatus { state, relay_version, boot_id }` |
| 6 | `Stop {}` | `Done`; the guest gets the power button and is stopped after 30 s |

Response types: 64 `Welcome`, 65 `Spawned`, 66 `SessionList`, 67
`MachineStatus`, 68 `Done`, 69 `Failed { error }`.

## File sharing control

`toby internal fs` answers on the machine's `fs-control.sock`. The first
request is `Hello`; requests are answered in order. Attachments appear in
the file share at `/projects/<id>`; IDs are 1 to 64 letters, digits, `-`
or `_`.

| Type | Request | Response |
| --- | --- | --- |
| 1 | `Hello { versions }` | `Welcome { version }` |
| 2 | `Add { id, host_path, read_only }` | `Done`; adding the same attachment again succeeds, the same ID with another path or mode fails |
| 3 | `Remove { id }` | `Done`, also when the ID is not served |
| 4 | `List {}` | `Attachments { attachments: [{ id, host_path, read_only }] }` |

Response types: 64 `Welcome`, 65 `Done`, 66 `Failed { error }`, 67
`Attachments`.

## Host services

A guest connection that reaches a host service through a capability (the
models proxy on `proxy.sock`, tobyd's `capability.sock`) starts with one
frame from `toby internal machine` naming the machine; the service's own
protocol (HTTP for the models proxy) follows. Services accept connections
only from processes of their own user.

| Type | Header |
| --- | --- |
| 1 | `FromMachine { machine_id }` |
