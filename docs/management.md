# Storage and Agent Management

Toby exposes direct commands for its per-user volumes, OCI image catalog, and
background agent. Image and volume commands do not load launch configuration,
so a broken application configuration does not prevent storage inspection or
repair.

Run `toby <group> <command> --help` for the generated flag reference. This
guide describes selection, output, concurrency, and deletion semantics.

## Common terminal behavior

Image and volume lists use a styled Bubble Tea table when stdout is a terminal
and a stable plain table when redirected. Native filesystem paths are excluded
from list output.

Interactive destructive commands show the complete selected table and ask for
confirmation. The current item uses an inline spinner; every completed,
untagged, removed, or failed item remains in the terminal. `--force` skips
confirmation and is required when confirmation input or output is not a
terminal. Redirected removal prints only the full IDs successfully processed.

`list` accepts the `ls` alias, and `remove` accepts `rm`, for both storage
groups.

## Volumes

Persistent home and tool state uses this per-user layout:

```text
$XDG_DATA_HOME/toby/volumes/<volume-id>/metadata.json
$XDG_DATA_HOME/toby/volumes/<volume-id>/_data/
```

`XDG_DATA_HOME` defaults to `~/.local/share`. See
[architecture.md](architecture.md#native-storage) for identity hashing,
ownership boundaries, seeding, and lifecycle leases.

### Create

Create an empty migration destination without launching a tool:

```sh
toby volume create --type home --name <environment> [--profile <profile>]
toby volume create --type tool --name <tool> --purpose <purpose> [--profile <profile>]
```

The profile defaults to `default`. Creation derives the canonical metadata ID,
atomically publishes an empty `_data`, and prints the full ID. Repeating the
same complete specification returns the existing volume without replacing its
contents.

### List and filter

```sh
toby volume list \
  [--type home|tool] \
  [--name <exact-name>] \
  [--profile <exact-profile>] \
  [--purpose <exact-purpose>]
```

The table contains the short ID, type, name, profile, purpose, and validation
status. Every supplied metadata option is an exact-match filter; an omitted
field matches any value. Invalid published objects remain visible with status
`invalid` so they can still be inspected or removed by ID.

### Inspect and print paths

Select a volume by full ID, an unambiguous lowercase hexadecimal prefix of at
least 12 characters, or one complete metadata specification:

```sh
toby volume inspect <volume-id>
toby volume inspect \
  --type home --name <environment> [--profile <profile>]
toby volume inspect \
  --type tool --name <tool> --purpose <purpose> [--profile <profile>]
```

`inspect` emits YAML by default. `-o json` and `--output json` select JSON; the
same flags accept `yaml`. Its document includes the full ID, object path,
metadata path, kernel-resolved `_data` path, metadata, and any validation
problem.

`path` accepts the same selectors and prints only the resolved `_data` path:

```sh
toby volume path <volume-id>
toby volume path --type home --name <environment>
```

This output is intended for scripts and migration commands.

### Remove

Remove explicit IDs:

```sh
toby volume remove <volume-id>...
```

Or remove the snapshot of all volumes matching metadata filters:

```sh
toby volume remove --type <type> \
  [--name <name>] [--profile <profile>] [--purpose <purpose>]
```

IDs and filter flags are mutually exclusive. A running launch holds a shared
lifecycle lease on each open volume. Removal takes a non-blocking exclusive
lease, so a volume with an active lease is refused rather than unlinked. `--force`
skips confirmation but does not override an in-use lease.

## OCI images

OCI data uses a per-user reference/object catalog beneath:

```text
$XDG_DATA_HOME/toby/images
```

A mutable normalized reference and exact platform point to an immutable object
containing the OCI layout, runtime bundle, extracted rootfs, and Toby metadata.
Multiple references may select the same object. An object without a reference
is `dangling`.

### Pull

```sh
toby image pull <reference>... \
  [--platform linux/<architecture>[/<variant>]]
```

The platform defaults to `linux/$GOARCH`. Each input is sent independently to
the per-user agent. Different images can prepare concurrently, while clients
requesting the same effective image attach to one preparation operation and
receive its current absolute progress snapshot.

Terminal output uses Toby's OCI progress rows. Redirected output is append-only
and reports periodic progress. `--debug` retains diagnostic output;
`--quiet` suppresses non-result progress. An image already complete in the
cache does not flash a transient progress row. Successful pulls print their
full reference-record IDs after presentation ends.

### List and filter

```sh
toby image list \
  [--reference <exact-normalized-reference>] \
  [--platform <os/architecture[/variant]>] \
  [--digest <sha256:manifest-digest>] \
  [--dangling[=true|false]]
```

The catalog shows one row per reference and one row per dangling object. `ID`
selects the catalog row or reference record. `IMAGE ID` is a displayed manifest
digest prefix and identifies the shared immutable object.

Filters match exactly. `--dangling` selects unreferenced objects, while
`--dangling=false` selects referenced rows.

### Inspect and print paths

```sh
toby image inspect <reference-or-id> \
  [--platform <platform>] [-o yaml|json]
toby image path <reference-or-id> [--platform <platform>]
```

References default to the current `linux/$GOARCH` platform. A full ID, an
unambiguous lowercase hexadecimal prefix of at least 12 characters, or a
manifest digest can select a stored object directly.

`inspect` emits YAML by default and accepts `-o json` or `--output json`. It
includes normalized reference and platform data, manifest and configuration
digests, aliases, runtime metadata, validation state, and native paths. `path`
prints only the kernel-resolved rootfs path.

### Remove and untag

Select explicit references, IDs, prefixes, or digests:

```sh
toby image remove <reference-or-id>... [--platform <platform>]
```

Or select by the same exact filters as `list`:

```sh
toby image remove \
  [--reference <reference>] \
  [--platform <platform>] \
  [--digest <digest>] \
  [--dangling[=true|false]]
```

Explicit selectors and non-platform filters are mutually exclusive. Removing
one alias removes only that reference. Removing the final reference also
removes the immutable object when no running sandbox holds it.

`--force` may remove a final reference while its object is in use. The running
sandbox keeps its descriptor and shared lease, and the object remains dangling;
the next launch using that reference must pull it again. Force never unlinks a
busy object.

### Prune

```sh
toby image prune [--force]
```

Prune selects only dangling objects. `--force` skips confirmation but does not
override an active object lease. When no dangling objects exist, the command
prints `No dangling images.`

## Per-user agent

A launch normally starts one agent at:

```text
$XDG_RUNTIME_DIR/toby/agent.sock
```

The agent owns OCI preparation, resource leases, local and remote MCP
backends, models discovery, resource logs, and the Caddy models route. It does
not own foreground application processes or their terminal streams.

Packaged systemd units can instead own the socket and keep the agent running.
The user unit uses the normal path above. The system-wide template uses:

```text
/run/toby/users/<username>/toby/agent.sock
```

Clients use the normal runtime socket when it exists and otherwise fall back to
the system-wide socket for the current username. The unused endpoint does not
block an agent from starting on the selected endpoint. See
[installation.md](installation.md#systemd-agent-activation) for unit commands
and precedence.

The following inspection and lifecycle commands do not invoke Toby's detached
agent launcher and do not load launch configuration:

```sh
toby agent status
toby agent resources
toby agent logs <resource-id> [--operation <operation-id>]
toby agent stop
tobyd [--persistent]
```

Connecting through an active systemd socket can still activate its matching
service, including for `status` or `stop`.

`status` reports the agent binary version, state, and active session, lease,
resource, and stream counts. `resources` prints resource kind, opaque resource
ID, and active lease count without configuration. A zero-lease row is an
in-progress operation or a runtime retained during warm idle.

`logs` prints the latest retained JSONL log for a resource, or one exact
operation or generation selected by `--operation`. See
[protocols.md](protocols.md#resource-logs) for paths, retention, records, and
streaming behavior.

Resource-log persistence is optional for the resource operation. A write,
sync, retention, or close failure emits a debug diagnostic and the resource
continues without that persisted history. The `logs` command itself reports an
error when its requested retained file cannot be opened or copied to stdout.

`stop` sends a graceful request. Connected launch clients receive a 17-second
cleanup deadline and send `SIGINT` to their foreground sandboxes. The sandbox
has 12 seconds for graceful exit before bounded `SIGTERM` and `SIGKILL`
escalation. Clients then release capabilities and leases and acknowledge the
agent. The agent retains a 20-second private client deadline, then bounds
gRPC drainage by five seconds and agent-owned resource teardown by ten
seconds. With socket activation, the listening unit remains active and a later
client can start a new agent. Stop both the socket and service units to disable
activation.

The normal agent exits after its startup grace when never used, or after its
final session, lease, resource runtime, and stream disappear. Warm-idle
resources and active OCI operations defer exit. `tobyd --persistent` disables
idle auto-stop; both packaged service units use it.

These models commands do load host configuration so they can resolve a
configured models resource:

```sh
toby agent models <configured-name-or-resource-id>
toby agent cache flush <configured-name-or-resource-id>
```

`models` discovers or returns cached model documents for one effective resource
and prints one tab-separated model ID and JSON document per line. Successful
discovery is cached for five minutes. `cache flush` invalidates that one
resource's discovery cache.
