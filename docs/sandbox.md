# Sandbox and Integration Details

Toby runs every application and local sidecar directly under Bubblewrap. An
environment is a persistent private-home identity, not a long-lived process.
Launching another application for the same environment creates another
Bubblewrap run that shares only the intended persistent storage.

## Filesystem layout

The application sees:

| Path | Contents |
| --- | --- |
| `/toby/home` | Persistent private home for the environment |
| `/toby/workspace/<name>` | Selected project directories |
| `/toby/bin/tobys` | Toby sandbox helper, mounted read-only |
| `/run/toby/sandbox.sock` | Exact run-scoped sandbox capability |
| `/etc/resolv.conf` | Snapshot of the host resolver configuration |
| `/etc/hosts` | Snapshot of the host name mappings, when present |
| `/etc/localtime` | Snapshot of the host timezone data, when present |
| `/tmp` | Fresh tmpfs |
| `/proc` | Fresh PID namespace process view |

The configured OCI rootfs supplies the rest of `/`. Its source may be a
registry reference, an OCI image-layout archive, or a Dockerfile build. Toby
mounts the published rootfs as the lower layer of a unique writable overlay
for the run. Setup commands and the foreground tool in one run reuse that
overlay; a concurrent run receives a different overlay. Toby holds each
foreground init at Bubblewrap's launch gate while it retains that exact
process and mount namespace. After the command, Toby waits for the init to exit
and closes the retained namespace descriptor before the next command reuses
the same upper and work directories.

Before Bubblewrap starts, Toby reads `/etc/resolv.conf`, `/etc/hosts`, and
`/etc/localtime` through ordinary host filesystem semantics and writes their
contents into the root overlay as regular files. A host symlink is followed by
the read; the symlink itself is not reproduced. `/etc/resolv.conf` is required,
while absent hosts and timezone files are omitted. Application runs, host-mode
local MCP sidecars, and Caddy each receive their own run-scoped snapshot.
Private local MCP sidecars instead receive a regular `/etc/resolv.conf`
containing only `nameserver 198.51.100.53`; Pasta forwards that synthetic
address to the resolver selected from the host configuration.

`HOME` is `/toby/home` and `TOBY_SANDBOX=1`. Toby clears the ambient host
environment and installs only validated run variables. Tool-specific secrets
are not inherited from the host.

## Private homes and projects

The environment name and launch profile select a private home. They are
independent from the application:

```sh
toby opencode work
toby codex work
```

Both use the same persistent `/toby/home` when their profiles also match, even
when they run concurrently. They need not use the same projects:

```sh
toby opencode work --project ~/Projects/frontend
toby codex work --project ~/Projects/backend
```

The project root defaults to `${XDG_PROJECTS_DIR:-~/Projects}`. Relative direct
`--project` paths resolve from that root. Resolved direct paths must remain
within it unless `settings.allowExternalProjects` is enabled; a launch file may
explicitly name additional sources elsewhere. Projects are mounted at
`/toby/workspace/<name>`.

Multiple project and tool selections are described in
[configuration.md](configuration.md#launch-configuration).

## Persistent storage

Toby-owned persistent state is ordinary native filesystem data beneath
`$XDG_DATA_HOME/toby`. Toby follows ordinary host symbolic links while walking
configured XDG parent paths and relies on kernel access checks there. Missing
parents are created with the process umask. At each final Toby-owned `toby`
root, Toby follows a configured symlink without changing it and pins the
resolved directory before operating beneath it. Toby best-effort assigns that
directory to the effective user and group and restricts it to mode `0700`.
Failed repairs emit a debug diagnostic and continue. The filesystem decides
whether each requested operation is permitted.

The private home is the `_data` directory of a volume identified by the
environment name and launch profile. Tools can also request volumes with
stable tool/profile/purpose identities. Tool volumes are global to the host
user: matching profiles reuse the same native state across private homes and
projects. A per-tool profile override changes only that tool volume.
Toby best-effort restricts each volume object directory to mode `0700`, but
does not validate or change the ownership or mode of `_data`; those attributes
belong to the sandboxed application.

An external bind is different: Toby verifies and exposes an existing path but
does not own its contents. Toby follows ordinary filesystem symlinks, rejects
magic-link traversal, and pins the resolved source and its exact parent with
`openat2`. Directory-source ancestry, or the exact parent ancestry for a
non-directory bind, must remain disjoint from protected and Toby-owned storage.
Non-directory binds must have exactly one hard link.

Managed paths are opened with descriptor-rooted filesystem operations. Toby
checks directory or file type, path containment, and inode identity before
passing a descriptor to Bubblewrap. Ownership and DAC mode are left to the
filesystem's ordinary access checks. Requests with conflicting keys or
overlapping sandbox targets fail before any launch.

Some tools seed a new tool volume from an image path. Toby copies that content
once from the exact leased immutable rootfs and atomically publishes the
completed volume. The canonical tool metadata determines the volume identity.

## Generated tool files

Toby writes generated configuration directly at each application's native
private-home paths. Each file is a complete Toby-owned document and is replaced
atomically at launch. A later launch replaces any manual edits to these files.

Concurrent launches sharing a home or tool volume use last-launch-wins
behavior for generated files. Toby does not lock persistent application
storage to one launch.

Substantial installers and wrappers are embedded static assets and mounted
transiently beneath `/run/toby`; they are not generated configuration.

## Namespace and capability policy

Application commands receive fresh user, PID, IPC, and UTS namespaces and
share the host network namespace. The host resolver snapshot provides the
configuration for that network independently of the selected OCI rootfs.

Bubblewrap drops application capabilities. Lifecycle commands that must run as
namespace root receive only the ownership, DAC, and identity-transition
capabilities needed by tool installers. They do not receive host root
authority. The `tobys` startup guard distinguishes this identity through
`/proc/self/uid_map`: namespace UID zero is accepted only when it maps to a
nonzero parent UID.

The plan mounts a controlled `/dev`, new `/proc`, tmpfs `/tmp`, and fresh
`/run`. Protected paths cannot be claimed by tool mounts.

Host networking is not treated as an authorization boundary. MCP uses a
run-scoped owner-only Unix socket. Models access uses a random route and
synthetic credential.

## Terminal behavior

Toby supports:

- noninteractive stdin/stdout/stderr pipes;
- direct terminal ownership; and
- a managed PTY that preserves normal job-control behavior while Toby presents
  approval prompts.

Managed terminal mode is enabled by default. Set
`settings.managedTerminal: false` or pass `--managed-terminal=false` for a
plain passthrough. Without a managed terminal, an operation that needs a prompt
and is not already allowed is denied.

The invoking CLI owns foreground bytes and signals. The agent is not in the
terminal path. The trusted payload shim passes the CLI a pidfd for the exact
application process before immediately replacing itself with that application.
The CLI uses this identity for graceful signals. It separately retains the
Bubblewrap process group with a pidfd, uses
`PIDFD_SIGNAL_PROCESS_GROUP` for forced tree teardown, and uses
`waitid(P_PIDFD)` for terminal stop and continue state. This requires Linux 6.9
or newer. Unsupported operations fail when the real foreground process needs
them.

The CLI catches `SIGINT` and `SIGTERM`. During startup it finishes the active
top-level operation or tool lifecycle action and exits at the next checkpoint.
After foreground handoff it sends a client-mediated signal directly to the
retained application process. An agent shutdown request sends `SIGINT` and
allows 12 seconds for the application to exit while Bubblewrap continues
monitoring it. If the application remains alive when the client's escalation
window begins, cancellation sends `SIGTERM` to the complete Bubblewrap process
group, waits two seconds, sends `SIGKILL`, and bounds reap by another two
seconds.

Startup status, warnings, and Toby diagnostics use stderr. The CLI stops its
interactive presentation and suppresses further Toby diagnostics before
starting the foreground process, then gives the application its original
stdin, stdout, and stderr. Documented non-foreground command results continue
to use stdout where they form the command's intended output.

## Per-user agent

The normal agent endpoint is:

```text
$XDG_RUNTIME_DIR/toby/agent.sock
```

Toby creates its directory with mode `0700` and its socket and election lock
with mode `0600`, subject to filesystem support. Existing ownership or mode
does not block startup. Filesystem access to the socket is the agent transport
boundary. A packaged system-wide socket may instead provide
`/run/toby/users/<username>/toby/agent.sock`. Clients prefer the normal
runtime endpoint when it exists and use the system-wide endpoint only as a
fallback. Otherwise, a launch starts the installed `tobyd` executable as a
detached process when no compatible agent exists. The unused
endpoint does not participate in that agent's election. That process exits
after all sessions, leases, resources, and streams are gone;
`tobyd --persistent` keeps it available for a service unit.

Concurrent starters use a short election lock only to select one agent. This
does not serialize applications, homes, projects, or tools.

The agent owns stable OCI, MCP, and models resources with independent leases.
Closing a launch agent session releases every lease acquired through that
session. It never receives the user's foreground terminal or standing host Git
credentials.

The agent socket itself is never mounted into an application or sidecar.
See [protocols.md](protocols.md) for the exact agent and sandbox gRPC
contracts, stream ordering, limits, and models HTTP capability.

## MCP integration

Every supported tool receives stdio MCP definitions:

```text
/toby/bin/tobys resource connect -- <client-resource-id>
```

The connector reaches the `toby.sandbox.v1` gRPC service at the fixed sandbox
path `/run/toby/sandbox.sock`. It first invokes `Hello` and verifies the
endpoint's sandbox protocol version, then sends the per-launch client resource
UUID and relays MCP bytes without interpreting them. The launch CLI owns the
exact socket generation, translates the UUID through its active agent lease,
and opens an independent `ConnectMCP` agent gRPC stream. The same sandbox
service can select registered models resources; tools consume models
through the launch's loopback HTTP capability.

The connector does not know:

- the agent socket;
- upstream MCP URLs or headers;
- local MCP commands or environment values;
- host mount paths; or
- models API credentials.

### Built-in Toby MCP

The built-in `toby` target starts a fresh MCP server session for each connector.
It exposes host Git operations and bounded documentation/session resources.
The MCP tool names are `git_commit`, `git_fetch`, `git_push`, `git_rebase`,
`git_tag`, and `resources_read`. Approval rules still use the host-action ids
`git.commit`, `git.fetch`, `git.push`, `git.rebase`, and `git.tag`.

Host Git requests return to the launch process as typed host-action messages on
the client-opened `OpenSession` gRPC stream. They are sent only in response to a
request initiated through that launch's sandbox connector. The launch process
resolves repositories, uses host credentials and signing configuration, and
applies approval policy. The agent has no standing Git authority.

Session introspection contains sandbox-visible projects, mounts, tools, models
summaries, and MCP status. It excludes host paths, capability paths, upstream
endpoints, headers, commands, environment values, and credentials, including in
debug mode.

### Local stdio MCP

A local stdio target runs one fresh sidecar process per connector. The process
is not restarted for each application command; it lives until that MCP
connector closes.

OCI preflight prepares the configured image before the application starts.
The process runs under Bubblewrap. Setup options and environment values are
passed through a sealed descriptor. The command follows Bubblewrap's `--`
separator in the process arguments.

`network: host` shares the host network namespace. `network: private` creates
a private namespace and starts one host Pasta process for that sidecar through
`nsenter`. Toby holds the Bubblewrap payload until Pasta has configured the
namespace. Private sidecars receive outbound DNS and Internet connectivity
without host-loopback access or forwarded ports. Pasta exits and is reaped with
the sidecar; an unexpected Pasta exit invalidates the resource generation.

### Local HTTP MCP

A local HTTP target listens on a Unix socket directly beneath `/run/toby` in
its sidecar sandbox. Matching resource definitions may share one process
generation across permitted scopes. Each connector still owns an independent
Streamable HTTP session.

After the final connector closes, the configured idle timeout controls
teardown even while a launch remains registered. A later connector restarts
the resource transparently through the same lease. Readiness failure or process
exit invalidates that generation; the resource registry owns bounded retry and
reaping.

Local HTTP sidecars use the same `host` and `private` network policies as local
stdio sidecars. Their MCP endpoint remains a Unix socket beneath `/run/toby`;
the network policy controls connections initiated by the MCP server.

### Remote HTTP MCP

Remote endpoints and headers remain agent-side. Each stdio connector receives
its own Streamable HTTP session to the remote service. The HTTP bridge enforces
bounded requests, responses, headers, bodies, concurrency, and session state.

## Models integration

Models-resource definitions stay on the host. Each definition receives one
independent agent resource and lease. Registration remains dormant; first use
installs its route in the shared Caddy generation.

The application receives:

- a loopback URL containing a random route;
- a separate synthetic credential;
- models-resource display metadata; and
- configured or discovered models.

It does not receive the real upstream URL or headers.

Caddy runs in its own read-only Bubblewrap sandbox from the official
`docker.io/library/caddy:latest` OCI image, which Toby pulls only when missing.
It has host networking for upstream access but no private home, project, or
application mounts. Admin, data, and authorization channels are separate
owner-only Unix sockets in agent-owned runtime storage.

For each request, the launch-owned loopback relay requires the synthetic
credential, translates its per-launch resource UUID through the active lease,
and opens an agent models stream. The agent-private Caddy route strips
client-controlled authentication and forwarding headers, applies fixed
host-held headers, and calls the configured upstream.

The loopback port is reachable by other same-host processes, so it is not
network isolation. The capability path and synthetic credential are the
authorization boundary. Neither reveals the real models API secret or grants
access to an agent or Caddy administration socket.

A process deliberately sharing the same private home or tool-volume profile can
read the latest generated models route and synthetic credential under
last-launch-wins behavior. It can use that active models route, but this still
exposes neither the real upstream credential nor an agent or Caddy
administration socket.

## Tool integration

### OpenCode

Toby writes:

- `~/.config/opencode/opencode.json`
- `~/.config/opencode/toby/opencode.json`
- `~/.config/opencode/AGENTS.md`

OpenCode config includes stdio MCP entries, models capability URLs and
synthetic credentials, and permission paths. Its config and data directories
are separate global tool volumes selected by profile, so projects and private
homes using that profile share ordinary native filesystem state and locking.

### Claude Code

Toby writes:

- `~/.config/claude/toby/mcp.json`
- `~/.config/claude/toby/settings.json`
- `~/.config/claude/CLAUDE.md`

Launch arguments point Claude at the generated MCP and settings files.

### Codex

Toby writes:

- `~/.codex/config.toml`
- `~/.codex/AGENTS.md`

It also supplies highest-precedence MCP launch overrides so project-level
configuration cannot shadow the run capability. The generated native config
marks every launch-selected `/toby/workspace/<project>` root as trusted so
replacing the file on each launch does not make Codex prompt for the same
project again.

### Copilot

Toby writes:

- `~/.copilot/mcp-config.json`
- `~/.copilot/copilot-instructions.md`

Launch arguments point Copilot at the generated MCP file and instruction path.

### Deep Agents Code

Toby writes:

- `~/.deepagents/.mcp.json`
- `~/.deepagents/toby/AGENTS.md`

### Grok

Toby writes:

- `~/.grok/plugins/toby-session/plugin.json`
- `~/.grok/plugins/toby-session/.mcp.json`
- `~/.grok/config.toml`
- `~/.grok/AGENTS.md`

Session MCP servers live in the generated plugin. Toby patches
`~/.grok/config.toml` so `[plugins].enabled` contains `toby-session`. Grok
receives the generated rule contents through its launch contract as needed.

### Cursor

Toby writes:

- `~/.cursor/mcp.json`
- `~/.cursor/rules/toby.mdc`

The generated MCP file is Cursor's global `mcp.json`. Replacing it on a later
launch drops servers that are no longer in the session. Combined instructions
are an always-applied Cursor rule. Launch arguments approve MCP connectors and
disable Cursor's nested sandbox. Linux login tokens live in
`~/.config/cursor/auth.json`. Cursor's config, state, and agent-data
directories are separate global tool volumes selected by profile.

## Docker tool exception

The built-in `docker` tool is the only Docker integration in Toby. Selecting it
deliberately exposes an owner-only per-run relay at
`/run/toby/docker.sock` and binds the host user's `~/.docker` read-only at
`/toby/home/.docker`. It requests no Toby volume. `DOCKER_HOST` selects that
relay. The launch process retains host authority to connect to the real Docker
socket, including supplementary group access, but Bubblewrap binds only the
pinned relay endpoint into the sandbox. The real host socket is never mounted.

This is a high-trust capability: control of the host Docker daemon can generally be
used to gain host-level control. Toby creates the relay and client-config bind
only when the `docker` tool is explicitly selected. The sandbox continues to
drop capabilities. Rootless Bubblewrap maps only the primary GID, but Linux
retains inherited supplementary kernel GIDs as unmapped credentials; they
appear as the overflow GID and can affect only host objects explicitly present
in the sandbox mount graph. Toby verifies that no supplementary GID is mapped,
and it never mounts the real Docker socket. The sandbox runtime, OCI store,
agent, MCP gateway, and models gateway do not use Docker.

## Debug information

Use `--debug` or `settings.debug: true` for raw launch subprocess output and a
debug-enabled safe session snapshot. Interactive launches normally use a
clearable inline presentation: concurrent OCI operations share one status line
followed by one image-transfer line per operation, while only the active
command's bounded output appears beneath them. Redirected launches use
append-only output with periodic percentage and byte-count updates. An
interactive startup failure clears
captured output and directs the user to rerun with `--debug`. Debug output
retains raw lifecycle subprocess streams; terminal debug launches retain OCI
progress bars, while redirected debug output is append-only. Use `--quiet` to
suppress non-foreground launch output without changing the foreground
application's streams. Debug and quiet modes are mutually exclusive.
`toby agent status` shows non-secret agent counts, while `toby agent
resources` lists opaque IDs and lease counts.
`toby agent stop` asks connected launch clients to stop their own foreground
sandboxes and release launch capabilities before the agent tears down its
resource leases and background resources. A systemd-owned socket can activate
a replacement agent on the next connection.

Debug output does not relax secret redaction or add host paths, capability
paths, upstream URLs, commands, headers, environment values, or credentials to
sandbox introspection.
