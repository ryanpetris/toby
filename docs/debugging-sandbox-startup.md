# Debugging Sandbox Startup

Native startup crosses these boundaries in order:

1. launch configuration shape and the agent hello/session handshake;
2. secret-bearing resource, project, and tool resolution;
3. Bubblewrap executable resolution;
4. agent-coordinated OCI resolution and rootfs preparation;
5. native project, home, mount, and run-directory preparation;
6. agent MCP/models registration, models discovery, and launch-owned
   capability creation; and
7. generated configuration and Bubblewrap run assembly.

The foreground application starts only after all seven succeed.

## Enable diagnostics

Launch debug mode preserves startup status and subprocess output:

Pass:

```sh
toby --debug opencode my-app
```

Or configure:

```yaml
settings:
  debug: true
```

Debug mode writes every startup status and startup subprocess stream directly
to its original destination. Real subprocess output is unbuffered and
byte-preserving: Toby does not normalize control sequences or hide output from
expected lifecycle probes. In a terminal, concurrent OCI operations use the
same inline progress rows as normal mode; those rows stop before direct
lifecycle subprocess output continues. Redirected debug output is entirely
append-only and reports OCI percentages and byte counts periodically. Debug
mode also marks safe session introspection as debug-enabled. It does not retain
the run overlay, and it does not reveal host paths, capability paths, commands,
environment values, upstream URLs, headers, or credentials through sandbox
introspection.

Process diagnostics have an independent structured logging configuration:

```sh
TOBY_LOG_LEVEL=debug TOBY_LOG_FORMAT=text toby opencode my-app
TOBY_LOG_LEVEL=debug TOBY_LOG_FORMAT=json toby opencode my-app
```

`TOBY_LOG_LEVEL` accepts `debug`, `info`, `warn`, or `error` and defaults to
`info`. `TOBY_LOG_FORMAT` accepts `text` or `json` and defaults to `text`.
Text records use `source: message`; JSON records include the level, source,
message, and structured attributes such as resource and operation IDs.
`--debug` controls launch presentation and subprocess streams, while
`TOBY_LOG_LEVEL=debug` enables internal cleanup, optional persistence, and
permission-repair diagnostics.

Warning records are registered conditions that can be suppressed through
`settings.suppressWarnings`. Text output begins with
`warning[<id>]:`; JSON output carries the same ID in `warning_id` together with
condition-specific structured attributes. Error records identify unexpected
failures of process-wide capabilities that are not returned directly to a
caller. Per-request and per-connection failures already visible through a
returned error, HTTP response, or closed stream are retained as debug records.

Diagnostics, warnings, and startup presentation use stderr. Documented
management results and foreground application stdout use stdout. At foreground
handoff Toby permanently suppresses its own diagnostic and status output for
that process so it cannot interrupt the application streams. The diagnostic
sink never turns an output failure into an application error.

Without `--debug`, an interactive launch presents the current startup
operations as separate rows. OCI rows include a spinner, image reference,
progress bar, percentage, and byte count. At most ten wrapped lines of output
belonging to the active command appear below the rows. A tool lifecycle action
is the parent of each sandbox command it starts. While a command runs, its row
and output temporarily replace the parent row; completing it restores the
parent until the lifecycle action ends or another command starts. Both rows
inherit the tool's display-name scope, such as `OpenCode: Installing` and
`UV: Preparing storage`. Command output uses a subdued color while
status rows retain the terminal's normal color. If agent work is concurrent,
completing the visible command returns to the newest operation still running;
output from another operation is never shown under the current label. A
Dockerfile build shows Buildah output while it runs, then clears that captured
command transcript when extraction progress begins. Debug and redirected modes
remain append-only and preserve the Buildah output. A successful startup
clears the presentation before the application starts. A
failed startup also clears it and reports only
`Toby startup failed. Re-run with --debug for details.` When stderr is not a
terminal, startup output is append-only without Bubble Tea controls.

For scripts that require only the foreground application's output, pass:

```sh
toby --quiet exec my-app -- command
```

Quiet mode suppresses startup statuses, warnings, lifecycle subprocess output,
OCI transfer progress, and agent acquisition progress. It works with terminal
and redirected launches. Fatal launch errors still appear once, and foreground
stdin, stdout, and stderr are not filtered or redirected. `--quiet` and
`--debug` are mutually exclusive.

During a slow launch, normal and debug progress identifies each required OCI
image by reference. The agent persists absolute operation-scoped resolving,
download, and extraction checkpoints before sending them to the launch. A
second launch requesting the same image attaches to the existing operation and
receives its latest snapshot immediately. Quiet launches still request
preparation but discard its presentation output. MCP and models runtimes start
lazily after launch and write their startup output to resource generation logs.

## Check host prerequisites

Toby requires Linux:

```sh
uname -s
uname -m
uname -r
```

The kernel must be Linux 6.9 or newer. Foreground cleanup and signal forwarding
use `PIDFD_SIGNAL_PROCESS_GROUP`. A kernel that does not implement the required
operation reports the error when Toby starts or controls the real foreground
process.

Confirm Bubblewrap is installed:

```sh
command -v bwrap
bwrap --version
```

Confirm the runtime directory resolves to an accessible location:

```sh
printf '%s\n' "$XDG_RUNTIME_DIR"
stat -c '%U %G %a %n' "$XDG_RUNTIME_DIR"
```

`XDG_RUNTIME_DIR` must be an absolute, clean path. Symbolic links and existing
parent-directory modes are accepted; ordinary kernel access checks determine
whether Toby can create or use its subdirectory. Toby does not chmod those
parents or configured symlinks. The resolved `$XDG_RUNTIME_DIR/toby` directory
is best-effort assigned to the effective user and group and restricted to mode
`0700`. A failed repair emits a debug diagnostic and continues; a failing
create, open, lock, bind, or connect operation reports the underlying kernel
error.

Bubblewrap also needs unprivileged user namespaces. Distribution policy can
disable them independently of the installed executable. Toby does not run
speculative namespace, mount, terminal, or process-lifecycle probes. The real
Bubblewrap command reports the kernel or filesystem error if a requested
operation is unavailable; use `--debug` to preserve its complete output.

## Check the selected image

Application launches default to
`mcr.microsoft.com/devcontainers/javascript-node:24-bookworm` with
`pull: if-missing`. Override either value in host configuration when needed:

```yaml
sandbox:
  image: docker.io/library/node:24-bookworm-slim
  pull: if-missing
```

An `archive` source reads an OCI image-layout tar. A `build` source invokes
`buildah` and writes an OCI archive:

```yaml
sandbox:
  build:
    context: .
    dockerfile: Dockerfile
  pull: if-missing
```

Archive sources use a deterministic reference derived from the resolved path
and platform. Builds use
`toby.local/<profile>/<primary-project>:<source-hash>`, where the hash covers
the resolved context and Dockerfile paths and platform. With
`pull: if-missing`, a published reference skips source materialization and
extraction. Toby does not inspect Dockerfile, context, or archive contents for
changes. Set `pull: always` when the source must be imported or built again.

Use `--image` and `--pull` to isolate configuration precedence:

```sh
toby opencode my-app \
  --image docker.io/library/node:24-bookworm-slim \
  --pull always
```

Typical OCI failures identify:

- an invalid registry reference;
- a registry/authentication or network failure;
- a digest mismatch;
- a missing platform manifest for `linux/$GOARCH`;
- a missing cached object under `pull: never`;
- a registry transfer failure;
- an invalid OCI archive;
- a missing or failing `buildah` command; or
- a rootless extraction failure.

Application sandboxes share the host network and receive the host
`/etc/resolv.conf` as a read-only bind. If DNS fails inside a sandbox, compare
that file with the host and preserve Bubblewrap output with `--debug`.

Toby's operation label names the image reference throughout preparation:

```text
Extracting OCI image docker.io/library/example:latest...
```

The interactive renderer preserves that full status update internally but
displays the operation and transfer separately:

```text
⠹ Preparing OCI images
  docker.io/library/example:latest     Extracting  ━━━━━╸━━━━  50%  128.0/256.0 MiB
```

The transfer line reserves 36 columns for the image reference. Toby first
removes complete path segments from the left, retaining the final image name,
and only then truncates the remaining text from the right. For example, a long
reference may appear as `.../javascript-node:24-bookworm`. Append-only modes
continue to print the full reference.

Per-user image data is under:

```text
$XDG_DATA_HOME/toby/images
```

with `XDG_DATA_HOME` defaulting to `~/.local/share`. Each immutable object
contains a verified OCI layout, an extracted bundle/rootfs, and schema-1 Toby
metadata; an atomic reference record selects the object for a mutable tag. Do
not manually alter an active object. Toby publishes complete objects atomically.

Inspect or manage the catalog without loading launch configuration:

```sh
toby image build [context] --output <archive> [--file <Dockerfile>] [--platform <platform>]
toby image import <archive> <reference> [--platform <platform>]
toby image pull <reference>... [--platform linux/<architecture>[/<variant>]]
toby image list [--reference <reference>] [--platform <platform>] [--digest <sha256:digest>] [--dangling]
toby image inspect <reference-or-id> [--platform <platform>] [-o yaml|json]
toby image path <reference-or-id> [--platform <platform>]
toby image remove <reference-or-id>... [--platform <platform>] [--force]
toby image prune [--force]
```

`image build` leaves the OCI archive outside the catalog. `image import`
publishes the selected platform under the supplied normalized reference.
`image list` omits paths; `image path` prints only the resolved rootfs path.
`image inspect` includes both reference and object metadata. A dangling row is
an immutable object without a current reference. Normal final-reference
removal refuses an object used by a running sandbox. Forced removal may untag
that reference while leaving the busy object dangling, but neither forced
removal nor prune unlinks a busy object. `image list` and `image remove` also
accept the `ls` and `rm` aliases.

## Check project resolution

The default project is:

```text
${XDG_PROJECTS_DIR:-~/Projects}/<environment>
```

Inspect the path:

```sh
realpath "${XDG_PROJECTS_DIR:-$HOME/Projects}/my-app"
```

Relative direct `--project` sources resolve from `XDG_PROJECTS_DIR`. Every
resolved source must remain at or below that root unless
`settings.allowExternalProjects` is enabled. Launch-file projects may
explicitly name other sources. `--project` changes the source, not the
private-home identity:

```sh
toby exec review-home --project ~/Projects/my-app -- pwd
```

For `.toby/config.yaml`, all relative project paths resolve from the project
root. For any other `--config` file, they resolve from the selected file's
directory. Config-set errors name the base or fragment that failed.

## Check native storage

Persistent locations are:

```text
$XDG_DATA_HOME/toby/volumes
```

Inspect the managed objects and their kernel-resolved host paths with:

```sh
toby volume create --type home --name <home> [--profile <profile>]
toby volume create --type tool --name <tool> --purpose <purpose> [--profile <profile>]
toby volume list [--type <type>] [--name <name>] [--profile <profile>] [--purpose <purpose>]
toby volume inspect <volume-id>
toby volume inspect --type <type> --name <name> [--profile <profile>] [--purpose <purpose>]
toby volume path <volume-id>
toby volume path --type <type> --name <name> [--profile <profile>] [--purpose <purpose>]
```

The list includes type, name, profile, purpose, and validation status.
Metadata options filter the list exactly; unspecified fields match any value.
`inspect` includes all native paths as YAML by default and accepts
`-o json` or `--output json`; `path` prints only the real `_data` path for
scripts. Complete metadata specifications may be used instead of an ID; their
profile defaults to `default`. `create` publishes an empty, idempotent migration
destination before first launch use. `volume list` and `volume remove` also
accept the `ls` and `rm` aliases.

An invalid object can still be selected by its displayed ID. Removal accepts
multiple IDs or metadata filters, shows all matches in an interactive
confirmation table, shows inline progress while deleting, and retains a
completed terminal row for every removed volume. It refuses volumes retained
by running launches. Use `--force` to remove without confirmation or from a
nonterminal; redirected removal prints only removed volume IDs.

Transient run overlays are:

```text
$XDG_CACHE_HOME/toby/runs
```

Configured XDG storage parent paths may contain symbolic links and may use any
existing mode that gives the user sufficient access. Toby does not change those
parents or the symlinks and creates missing parents with the process umask. It
follows a final `toby` symlink, pins the resolved Toby-owned root, and
best-effort assigns it to the effective user and group with mode `0700`.
Failed repairs emit debug diagnostics. Below that boundary Toby rejects
replacement, inode changes, malformed volume objects, conflicting tool-volume
keys, overlapping sandbox targets, and symlink pivots. Volume object
directories receive the same best-effort repair; `_data` ownership and mode
are application-controlled and are not validated or changed.

A Bubblewrap invocation itself is the filesystem and mount compatibility
check. Toby does not parse its diagnostic text. The first invocation uses one
attempt. After a run has already used its writable overlay, Toby may repeat a
structured-status-proven pre-payload setup failure for a bounded interval while
the kernel finishes releasing the previous OverlayFS mount. A final failure
forwards Bubblewrap's stderr unchanged through the launch's normal stream mode
and reports that the process failed before payload execution.

Sequential setup commands reuse one run overlay. The Bubblewrap monitor can
finish before the sandbox init has completely released its mount namespace, so
Toby uses Bubblewrap's launch gate while it retains the init's exact pidfd and
mount-namespace descriptor. It then releases the gate, waits for the init to
exit, and closes the retained namespace descriptor before starting the next
command. When that wait is observable, startup progress changes to
`Finalizing...`.

Closing the descriptor releases Toby's last namespace reference, but Linux may
finish OverlayFS superblock cleanup asynchronously. If the next mount reaches
that narrow interval, Bubblewrap can still return `EBUSY`. Toby retries only
when JSON lifecycle status and the trusted payload-ready marker prove that no
payload ran. Transient attempt output is discarded; if the bounded retry window
expires, the final Bubblewrap error is shown exactly once.

The payload-ready marker also remains authoritative after startup. If a
terminal signal interrupts Bubblewrap before it can write its final JSON exit
event, Toby propagates the resulting status without claiming that the payload
never executed.

`SIGINT` and `SIGTERM` received during startup are observed at operation
boundaries. Toby lets the active status operation or tool lifecycle action
finish, then exits before beginning the next one. Once the foreground sandbox
starts, client-mediated graceful signals use the exact payload pidfd supplied
by the trusted exec shim. Forced teardown uses the separately retained
Bubblewrap process group. Debug output shows the last completed startup
operation and any Bubblewrap output from that operation.

A first-use seeded directory is published only after the complete image subtree
has been copied and synced. If seeding fails, the error identifies the image
path and destination key; no partial destination should be visible.

## Check the agent

Show non-secret state:

```sh
toby agent status
```

This command intentionally does not autostart a missing agent. A normal launch
does autostart one when needed.

List active resource IDs, kinds, and lease counts without configuration:

```sh
toby agent resources
```

A zero lease count means the agent still owns an in-progress OCI operation or
a backend retained for its warm-idle interval.

Print the latest retained log for one resource:

```sh
toby agent logs <resource-id>
```

Use `--operation <operation-or-generation-id>` to select an exact retained log.
OCI preparation and MCP/models startup write their external output to these
disk-backed logs.

Stop the agent and wait for its background resources to be torn down:

```sh
toby agent stop
```

This command does not invoke Toby's detached agent launcher. An active systemd
socket can still activate its matching service when the command connects.
Connected launch clients receive a bounded shutdown request, stop their own
foreground sandboxes, release their capabilities, and acknowledge cleanup.

Run it in the foreground for diagnosis:

```sh
tobyd --persistent
```

Without `--persistent`, the agent exits after its initial connection grace or
when its final session, lease, resource, and stream have disappeared.

The normal endpoint is:

```text
$XDG_RUNTIME_DIR/toby/agent.sock
```

When the normal socket is absent, the client can fall back to the packaged
system-wide socket at `/run/toby/users/<username>/toby/agent.sock`. Use
`systemctl status tobyd@<username>.socket tobyd@<username>.service` to inspect
that activation path.

If a protocol-incompatible or unhealthy process owns the socket, stop that
process before starting the current binary. Each client first invokes the
agent's `Hello` gRPC method. The response identifies the agent binary and
application protocol versions, and the client decides whether it supports that
protocol. Different Toby binary versions can share the agent when they
implement the same agent protocol version; Toby prints a warning containing
both binary versions. Do not expose or mount this socket into a sandbox.

See [protocols.md](protocols.md) for exact RPC message ordering, typed errors,
resource-log records, and stream half-close behavior.

Concurrent launch attempts use a short agent-election lock only. There is no
per-home or per-application lock; simultaneous applications for one private
home are supported.

## Diagnose MCP resources

MCP acquisition registers each target independently without starting its
backend. OCI preflight prepares local target images before the application
starts. The first connector lazily starts the selected backend; concurrent
first connectors join that startup generation.

For a local target, check:

- the OCI reference has a Linux manifest for the host architecture;
- the command exists in that image;
- environment names are valid and do not override `HOME` or `TOBY_SANDBOX`;
- `scope` is `user`, `home`, `project`, or `run`;
- `network` is `host` or `private`; and
- every mount source is an absolute, safe host path.

For local HTTP, also check:

- `endpoint.kind` is `unix`;
- the socket is directly beneath `/run/toby`;
- the command actually creates that socket; and
- `endpoint.path` matches the server's Streamable HTTP route.

For remote HTTP, check the URL and host-side substitutions. Remote entries may
not contain local-only image, command, endpoint, mount, scope, network, or idle
fields.

Applications always connect through:

```text
/run/toby/sandbox.sock
```

They do not connect to the agent socket or upstream endpoint directly.

Use `toby agent resources` to find the opaque `resource.mcp` ID, then
`toby agent logs <resource-id>` to inspect its latest generation log.

## Diagnose models resources

Models configuration requires `protocol` (`openai` or `anthropic`) and `url`.
Header substitutions are resolved on the host before acquisition.

List or discover models for one configured resource:

```sh
toby agent models local
```

Successful dynamic discovery is cached for five minutes. Flush that resource's
cache with:

```sh
toby agent cache flush local
```

Launches automatically request the model list for every configured models
resource because generated tool configuration requires the discovered model
map. The agent cache avoids repeated upstream requests by launches using the
same effective resource configuration.

The models gateway uses the official `docker.io/library/caddy:latest` OCI
image with an `if-missing` pull policy and run-scoped runtime sockets beneath
`$XDG_RUNTIME_DIR/toby/caddy`. The application receives only a loopback
capability URL and synthetic credential.

Do not test the application URL without its synthetic credential; rejection is
expected. Gateway-generated failures intentionally hide protected upstream
URLs, headers, and redirect locations.

## Diagnose foreground terminal behavior

Managed terminal mode is enabled by default. If a terminal application behaves
differently, compare:

```sh
toby --managed-terminal=false exec my-app -- your-command
```

The plain mode removes Toby's approval UI. Any action requiring a prompt and
not explicitly allowed is then denied; Toby emits
`permission.auto-deny` when applicable.

Managed-PTY mode is selected only when stdin, stdout, and stderr refer to the
same terminal. Terminal stdin combined with redirected output, or
`--managed-terminal=false`, selects direct-terminal mode so stdout and stderr
remain independently redirected.

Noninteractive and background Bubblewrap children detach from any controlling
terminal. If their configured stdin is a terminal, Toby replaces it with
`/dev/null`; input redirected from a regular file or pipe remains connected.
The agent is not involved in foreground I/O.

## Inspect one run safely

Useful host-side checks include:

```sh
ps -ef | rg 'toby|bwrap'
toby agent status
```

While a run is active, its overlay is beneath `$XDG_CACHE_HOME/toby/runs`.
Toby removes it during normal cleanup. Do not reuse an overlay upper/work pair
in another Bubblewrap command; it belongs to one run.

Inside the sandbox, safe checks include:

```sh
id
printf '%s\n' "$HOME"
pwd
mount
ls -la /toby /toby/home /toby/workspace /run/toby
```

Expected values include `HOME=/toby/home`, selected projects under
`/toby/workspace`, and a run-scoped sandbox socket at `/run/toby/sandbox.sock`.

## Docker tool failures

Docker is not a Toby runtime prerequisite. A Docker error is relevant only when
the `docker` tool was explicitly selected.

That tool requires:

- a `docker` CLI in the selected OCI image; and
- host-process access to `/var/run/docker.sock`.

Inside the sandbox, `DOCKER_HOST` points to the per-run relay at
`/run/toby/docker.sock`. If relay startup reports a permission error, verify
that the invoking host user can connect to `/var/run/docker.sock`; Toby does
not change the host socket's mode or group.

The tool is a high-trust exception. Normal launches neither probe the socket
nor require a Docker daemon.
