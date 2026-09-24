# Toby VM rewrite: implementation plan

Status: design agreed in discussion (2026-09-23/24). Not started.

This plan replaces the current Go/Bubblewrap/OCI implementation with a Rust
implementation that runs development tools inside KVM virtual machines. The
existing code is deleted and the repository is restarted as a Rust
workspace. Nothing from the old design is kept for compatibility; the pre-1.0
rules in `AGENTS.md` still apply (no shims, no dual paths, delete replaced
behavior completely).

Items marked **VERIFY** are assumptions to confirm in the milestone 0 spikes
before anything is built on them.

---

## 1. Goals and non-goals

### Goals

1. Every tool (Claude Code, Codex, OpenCode, …) runs inside a KVM virtual
   machine with its own kernel, as an ordinary user, with ordinary system
   services available (the image's own systemd is PID 1).
2. Images are built as **complete** root filesystems inside a builder VM.
   The host never unpacks layers, runs build steps, or writes into images.
3. Only **stock distro kernels** are used. Toby never compiles a kernel.
4. **Homes** (private `$HOME` disks) and **roots** (persistent, resettable
   writable layers over an image) are independent objects, many-to-many.
5. One running **machine** per (home, root) pair, shared by every tool
   launched against that pair. Project directories, forwards and tools are
   added and removed while it runs.
6. A small guest-side stable tier plus host-side controllers: the host
   daemon and all guest-side logic can be upgraded **without restarting any
   VM or interrupting running tools**.
7. The host daemon is a coordinator. All credentials (model provider keys,
   MCP secrets, git credentials) and all approval decisions stay on the host.
8. Port forwards in both directions, added and removed at runtime.
9. HTTP+JSON+WebSocket API shared by the CLI and a web UI.
10. Terminal UI with overlay approvals (default), floating windows and a
    status bar, built after the core.
11. Designed so a macOS back end (Virtualization.framework) can be added
    later without restructuring.

### Non-goals (this implementation)

- macOS support (design for it only).
- Windows hosts or non-Linux guests.
- Any Docker integration. Users who want Docker put it in their image.
- A root-level coordinator service, system units, or multi-user web access.
- Layer-aware image storage or cross-image deduplication.
- Live migration, snapshots.
- Migration from existing Toby installations. The new Toby does not read,
  convert, clean up, or even detect old config files, volumes, images or
  sockets from the current implementation.

### Explicitly dropped from the current Toby

Bubblewrap and everything built for it (run overlays, descriptor-rooted
publication, rootfs snapshots), host-side OCI unpacking (umoci), host
`buildah`, Caddy, the Docker socket relay, per-run capability UUIDs,
global tool volumes and tool-volume profiles, per-run root overlays, and
the current agent protocol.

---

## 2. Glossary (canonical terms for the new code)

| Term | Meaning |
| --- | --- |
| **image** | Immutable complete, bootable root filesystem: an ext4 filesystem inside a qcow2 file, plus the image's own kernel and a Toby-generated initramfs extracted next to it, plus an image config JSON (Env, User, WorkingDir, labels). Identified by a random ULID; records its source and architecture. |
| **boot adaptation** | The automatic step that turns any supported container image into a bootable Toby image: installs the distro kernel, dracut, systemd and sudo if missing, installs the `99toby` dracut module, generates the initramfs. |
| **image source** | mkosi configuration directory, Dockerfile build, registry reference, or OCI archive used to produce an image. |
| **root** | Named, persistent writable qcow2 overlay whose backing file is one exact image. Resettable. Many-to-many with homes. |
| **home** | Named, persistent ext4 disk mounted as the guest user's `$HOME`, plus the guest user identity (username, UID). |
| **default image** | The image built from the mkosi configuration bundled with Toby (§15.1). Used when no image is configured, and also used to boot builder machines. Toby never publishes prebuilt images. |
| **bootstrap image** | A pinned stock distro cloud image used exactly once per architecture to build the first default image (§15.2). |
| **machine** | A running VM for one (home, root) pair. Identified by a ULID machine ID. Holds exclusive write access to its home and root disks. |
| **services machine** | A machine with a root and a throwaway home, used to host isolated stdio MCP servers. |
| **builder machine** | A throwaway machine booted from the default image (or, once, the bootstrap image) to build images and format new disks. |
| **session** | One process tree started inside a machine (a tool, a shell, an `exec`). Survives CLI disconnects. |
| **attachment** | A host directory made visible inside a machine through the machine's virtio-fs device. |
| **forward** | A host↔guest TCP or Unix connection path, either direction. |
| **capability** | A forward created by Toby to a Toby service (models proxy, remote MCP proxy, Toby MCP, sandbox API). |
| **identity** | Who a guest command runs as: `User` (the home's user) or `Root`. Nothing else. |
| **stable tier** | Long-running processes that own VM processes, sockets, terminals and child processes and are not restarted on upgrade: `toby-machine`, `toby-fs`, `toby-relay`, `toby-session`, `toby-connect`. All are subcommands of the single `toby` binary (§3.3). |
| **control tier** | Replaceable processes that make decisions: `tobyd`, `toby` (CLI), `toby-helper`, `toby-proxy`. Also subcommands of `toby`. |
| **desired state** | Per-machine `machine.toml` written by `tobyd`; `toby-machine` continuously reconciles the running machine to it. |
| **back end** | How host processes are supervised: `systemd-user` (default) or `direct`. |

---

## 3. Architecture overview

```text
HOST (Linux, user's systemd instance or direct supervision)

 toby (CLI) ──HTTP/WS over unix──► tobyd (control plane, replaceable)
    │                                 │ writes machines/<id>/machine.toml
    │                                 │ reads  machines/<id>/status.toml
    │                                 │ ServiceManager: start/stop units
    │ session bytes (direct)          ▼
    └──────────────────────────► toby-machine@<id>   (stable: reconciler + byte shuttle)
                                   │  │  │
               control (unix) ─────┘  │  └───── vsock (hybrid, unix sockets)
                                      │                 │
                          toby-fs@<id> (virtio-fs)      │
                          toby-net@<id> (passt)         │
                          toby-vm@<id> (cloud-hypervisor)
 toby-proxy (models + remote MCP, replaceable)          │
                                                         ▼
GUEST (image's systemd = PID 1)
   toby-relay.service (stable)  ── spawns ──► toby-session (per session, stable)
   toby-helper (versioned, short-lived, run on demand)
   toby-connect (stable stdio↔vsock client for MCP)
   /run/toby/sandbox.sock, 127.0.0.1:<models port>  (guest ends of capabilities)
```

### 3.1 Tier rules

- **Stable tier** processes hold everything that must not be interrupted:
  VM processes, the virtio-fs back end, listening sockets, open streams,
  terminals and session child processes. They implement no policy, keep
  their protocols tiny and versioned, and are rarely changed.
- **Control tier** processes make all decisions and can be killed and
  restarted at any time. They rebuild runtime state from disk and from the
  stable tier.
- **One connection per stream.** Every session attachment, forward,
  capability request or MCP stream is its own vsock connection. Stable
  processes only splice bytes between two sockets after reading a small
  header, so there is no shared multiplexing state to lose on restart.
- **No bulk traffic through `tobyd`.** Terminal bytes, forward traffic,
  models traffic and MCP traffic never pass through `tobyd`. Restarting
  `tobyd` is invisible to running tools.
- **No long-lived smart process in the guest.** Every guest-side operation
  that needs logic (user setup, mounts, file patching, networking, port
  listing) runs as a short-lived `toby-helper` started from the current
  runtime version. Upgrading guest logic is instant.

### 3.2 Live upgrade behavior

| Component | How it is upgraded | Effect on running tools |
| --- | --- | --- |
| `tobyd` | restart (socket-activated); new process reads state files and reconnects to every `toby-machine` control socket | none; in-flight API calls retried by CLI |
| `toby-proxy` | restart (socket-activated) | in-flight model/MCP HTTP requests fail once; clients retry |
| `toby-helper` | new version directory served in the runtime tree; next invocation uses it | none |
| `toby` CLI | replace binary | reattach sessions |
| `toby-relay` | restart the guest unit (`systemctl restart toby-relay` via helper) | open forward/session bridges drop; sessions keep running; CLI auto-reattaches; `tobyd` re-registers listeners |
| `toby-machine` | restart unit | open forwards and session bridges drop; VM unaffected; reconciler restores listeners from desired state |
| `toby-session` | new sessions use the new binary; old sessions keep the old one | none |
| `toby-fs`, `toby-net`, cloud-hypervisor | new version applies at next machine start (restart when idle) | none until restart |

Version compatibility rules: every stable-tier protocol has a version
negotiated in the first message of every connection. The control tier must
support the current and the previous stable protocol version. The binary is
installed side by side by version (§3.3), so running stable processes keep
using the version they started with.

### 3.3 One binary

Everything is a single static binary, `toby`, with subcommands. The
component names used in this plan (`tobyd`, `toby-machine`, `toby-relay`, …)
are subcommands, not separate executables:

| Component | Invocation | Where |
| --- | --- | --- |
| CLI | `toby …` (user-facing commands, §20) | host |
| `tobyd` | `toby daemon` | host |
| `toby-proxy` | `toby internal proxy` | host |
| `toby-machine` | `toby internal machine --machine <id>` (`--supervise` in direct mode) | host |
| `toby-fs` | `toby internal fs --machine <id>` | host |
| VM / net launchers | `toby internal vm --machine <id>`, `toby internal net --machine <id>` (read desired state, `exec` cloud-hypervisor / passt) | host |
| `toby-relay` | `toby guest relay` | guest |
| `toby-session` | `toby guest session …` | guest |
| `toby-connect` | `toby guest connect <target>` | guest |
| `toby-helper` | `toby guest helper <op> …` | guest |

Rules:

- `internal` and `guest` subcommand groups are hidden from help and are not
  a stable user interface.
- Multi-call dispatch on `argv[0]`: when invoked as `toby-connect`,
  `toby-relay`, etc. (symlinks), the binary behaves as that subcommand. Tool
  configs use `/run/toby/bin/toby-connect` for readability.
- The Linux build is a fully static musl binary (rustls, pure-Rust D-Bus;
  no OpenSSL or libsystemd). Guest and host architecture are always the same
  under KVM, so the host's own binary is served to guests unchanged through
  the runtime tree (§9.4). A future macOS package additionally ships the
  Linux aarch64 build of the same binary for guests.
- Versioned install: packages install the binary as
  `/usr/lib/toby/<version>/toby` with `/usr/bin/toby` pointing at the
  current version. Unit files reference `/usr/lib/toby/current/toby` (a
  symlink updated on install), and every long-running stable process
  records which version it runs. Old version directories are removed only
  when no running machine or process uses them (`toby doctor --gc`, and
  automatically by `tobyd`).
- Stable-tier behavior is preserved by *not restarting* stable processes on
  upgrade, not by keeping their code in separate binaries. The stable
  subcommands must still depend on as little as possible and their
  protocols must stay versioned.
- Allocator: musl's allocator is slow for heavy async workloads; use
  `mimalloc` for the host subcommands (**VERIFY** static musl build in CI).

---

## 4. Host requirements

- Linux, x86_64 first; aarch64 designed in and enabled later (see §19).
- `/dev/kvm` readable and writable by the user (usually `kvm` group).
  `toby doctor` checks and explains.
- **Bundled** with Toby's packages (assume always present in package
  deployments):
  - Cloud Hypervisor, upstream static release binary, pinned version
    (≥ v51: qcow2 `backing_files=on`, discard/write-zeroes on qcow2, hybrid
    vsock, vhost-user net and fs), installed at
    `/usr/lib/toby/cloud-hypervisor`.
  - rust-hypervisor-firmware, upstream release binary, pinned version,
    installed at `/usr/lib/toby/firmware/hypervisor-fw` (arch-dependent, so
    `/usr/lib`, not `/usr/share`). Used only for the one-time bootstrap builder (§15.2).
  - mkosi, pinned release (Python source; LGPL-2.1-or-later), installed at
    `/usr/share/toby/mkosi/` and used only inside builder machines (served
    through the runtime tree), plus the default image's mkosi
    configuration at `/usr/share/toby/images/default/` (§15.1).
  - Pinned upstream release tags live in `packaging/bundled.toml` (e.g.
    `cloud-hypervisor = "v51.1"`, `mkosi = "v26"`); no content hashes are
    stored in Toby. Packaging CI downloads the release artifacts for those
    tags over HTTPS, checks them against upstream-published checksums or
    signatures when the release provides them, and installs them. Licenses (Cloud Hypervisor Apache-2.0 /
    BSD-3-Clause, firmware Apache-2.0, mkosi LGPL-2.1-or-later with its
    source) are installed with the package's
    license files (`/usr/share/licenses/toby/` on Arch,
    `/usr/share/doc/toby/copyright` on Debian).
  - Lookup: config override (`[programs] cloud_hypervisor = "…"`,
    `firmware = "…"`, for development and non-package installs), else the
    bundled path. No `PATH` lookup for bundled programs, so Toby always
    uses the version it was tested with.
- **System-installed** (package dependency, not bundled; GPL):
  passt with vhost-user support (**VERIFY** minimum version and that Cloud
  Hypervisor's `--net vhost_user=true` works with `passt --vhost-user`).
  Found via config override or `PATH`; version checked.
- Toby never downloads programs at runtime. A missing or too-old program
  produces an error naming it, the required version and the package to
  install; `toby doctor` lists everything.
- No `qemu-img`: qcow2 files are created in-process with the `imago` crate
  (§6.2).
- systemd user instance for the default back end. The `direct` back end
  needs nothing extra.

Guest image requirements: see §15.5. In short, any image from a supported
distro family works as-is because boot adaptation adds the kernel, dracut,
systemd and sudo; Toby's own binary is never installed into an image.

---

## 5. Repository reset

Milestone 1 deletes everything except `LICENSE`, `CLAUDE.md` (still points
at `AGENTS.md`) and git history, then creates:

```text
Cargo.toml                    (workspace)
rust-toolchain.toml           (pinned stable; edition 2024)
deny.toml                     (cargo-deny: licenses, advisories, bans)
AGENTS.md                     (rewritten for the Rust workspace; keeps pre-1.0 rules)
README.md                     (rewritten)
crates/
  toby-proto/                 wire formats: stream headers, relay control, session protocol, machine control
  toby-api/                   HTTP API types (serde + utoipa OpenAPI)
  toby-config/                config/manifest parsing, paths, secrets resolution
  toby-store/                 images, roots, homes, boot kits, locks, qemu-img wrapper, GC
  toby-engine/                VM engine trait + cloud-hypervisor implementation
  toby-svc/                   ServiceManager trait + systemd-user + direct implementations
  toby-vfs/                   virtio-fs server library (synthetic tree + passthrough mounts)
  toby-tools/                 tool manifests, templating, config patching
  toby-term/                  terminal handling now; compositor later
  toby-daemon/                tobyd (library)
  toby-machine/               per-machine stable host process (library)
  toby-fs/                    per-machine virtio-fs back end (library; uses toby-vfs)
  toby-proxy/                 models + remote MCP proxy (library)
  toby-guest/                 relay, session, connect, helper (library; no host-only deps)
  toby/                       the single binary: CLI + subcommand dispatch (§3.3)
packaging/
  systemd/user/               unit files (§12)
  dracut/99toby/              dracut module for the boot kit initramfs
  images/default/             bundled mkosi configuration for the default image (mkosi.conf,
                              mkosi.sandbox/ with the NodeSource apt source, mkosi.postinst.chroot),
                              installed to /usr/share/toby/images/default/
  bundled.toml                pinned upstream release tags of bundled artifacts
                              (cloud-hypervisor, firmware, mkosi)
docs/                         user docs (written as features land)
```

`plans/` (this plan and future ones) is kept in the repository.

Build and checks (replacing the Makefile targets; keep a thin Makefile or
`justfile`):

- `cargo fmt --check`, `cargo clippy --all-targets -- -D warnings`,
  `cargo test`, `cargo deny check`.
- The `toby` binary builds for `x86_64-unknown-linux-musl` (later
  `aarch64-unknown-linux-musl`), fully static; the same artifact runs on the
  host and in guests.
- KVM integration tests gated by `TOBY_TEST_KVM=1`.
- Fuzz targets (`cargo fuzz`) for every parser that reads guest-originated
  bytes (§17).

---

## 6. Storage layout

All persistent data is identical for both back ends. Only runtime paths
differ.

### 6.1 Paths

`storage_root` defaults to `$HOME/.local/share/toby` and state to
`$HOME/.local/state/toby`. Both are overridable in
`~/.config/toby/config.toml`. Toby resolves paths itself from `$HOME` and
its config file (not from `XDG_*` variables that may differ between
shells and services). The CLI compares its resolved paths with those
reported by `tobyd` and errors on mismatch.

```text
~/.config/toby/
  config.toml                 global config (§14)
  secrets.toml                0600; secret values or commands that print them
  tools/*.toml                user tool manifests (override/extend built-ins)

<state> = ~/.local/state/toby/
  images/<image-id>.json      image record: source, arch, created, size, config
  roots/<name>.toml           root record: image id, default for homes, created
  homes/<name>.toml           home record: username, uid, default root, created
  machines/<machine-id>/
    machine.toml              desired state (written only by tobyd)
    history.jsonl             lifecycle log
  approvals/<id>.json         pending and recent approvals
  builds/<build-id>.log       build logs

<data> = ~/.local/share/toby/
  images/<image-id>/
    disk.qcow2                immutable root filesystem
    vmlinuz                   the image's own kernel (extracted at build time)
    initramfs.img             Toby-generated dracut initramfs for that kernel
  roots/<name>.qcow2          backing file = images/<id>/disk.qcow2
  homes/<name>.qcow2
  builder/<arch>/
    bootstrap-<distro>-<version>.qcow2   stock cloud image, used once (§15.2); deletable afterwards
    cache.qcow2                          persistent /var/lib/containers for builds

runtime (systemd-user back end) = /run/user/<uid>/toby/
runtime (direct back end)       = $TOBY_RUNTIME_DIR or /tmp/toby-<uid>/ (0700, owner verified)
  tobyd.sock                  API socket
  proxy.sock                  toby-proxy
  capability.sock             tobyd capability endpoint (for toby-machine only)
  machines/<machine-id>/
    control.sock              toby-machine control
    session.sock              CLI session attach endpoint
    status.toml               observed state (written only by toby-machine)
    ch-api.sock               cloud-hypervisor API
    vsock.sock                hybrid vsock (host→guest connects)
    vsock.sock_1024           hybrid vsock (guest→host port 1024)
    fs.sock                   vhost-user socket for toby-fs
    net.sock                  vhost-user socket for passt
    console.log               guest console
```

Direct back end runtime under `/tmp`: `tobyd` touches the runtime directory
tree periodically so systemd-tmpfiles age-based cleanup does not remove it.

### 6.2 Disk formats and operations (`toby-store`)

- Image: qcow2 containing a bare ext4 filesystem (no partition table),
  label `toby-root`. Sparse, default virtual size 64 GiB (configurable per
  build). Never modified after the build completes; file mode 0444.
- qcow2 creation uses the `imago` crate (MIT; by a QEMU block-layer
  maintainer), `Qcow2::create_builder`, for both empty images and overlays
  with a backing file and explicit backing format `qcow2` (never probe
  backing formats). Tests validate output with `qemu-img check` where
  available (test-only dependency). **VERIFY** in M0 that the created
  overlays open in Cloud Hypervisor with `backing_files=on`.
- Root: qcow2 overlay `roots/<name>.qcow2` with backing file
  `images/<id>/disk.qcow2`, backing format `qcow2`.
  - `reset`: delete and recreate against the same image.
  - `rebase`: delete and recreate against a newer image. (Block-level
    overlays cannot move to a different image.) `toby root ls` marks roots
    whose image has been superseded for the same source.
  - Optional per-machine **ephemeral** layer: a temporary qcow2 overlay on
    top of the root, deleted when the machine stops.
- Home: empty qcow2 `homes/<name>.qcow2` (default 100 GiB virtual size,
  sparse), formatted as ext4 (label `toby-home`) by a short builder-machine
  job when the home is created, so images never need `mkfs` tools. The
  first boot of a machine using it copies `/etc/skel` and fixes ownership
  (§9.6).
- Garbage collection: an image is removable only when no root references
  it. `toby image rm` refuses otherwise; `toby image prune` removes
  unreferenced images older than a threshold.
- Locking: exclusive `flock` on the home and root files, held by
  `toby-machine` for the life of the machine. `tobyd` uses the locks plus
  the machine registry to enforce the pairing rules (§8.2).
- Cloud Hypervisor must be started with `backing_files=on` for root disks
  only; home and scratch disks keep it off.
- Toby host code only creates qcow2 containers (headers and empty tables)
  and never reads or writes guest data or parses guest filesystems; all
  data access is by Cloud Hypervisor.

---

## 7. VM engine (`toby-engine`, Cloud Hypervisor)

### 7.1 Engine trait (platform-neutral)

```rust
trait Engine {
    fn launch(&self, spec: &VmSpec) -> Result<VmHandle>; // process + API socket
    fn shutdown(&self, vm: &VmHandle, grace: Duration) -> Result<()>; // power button, then kill
    fn info(&self, vm: &VmHandle) -> Result<VmInfo>;
    fn add_disk(&self, vm: &VmHandle, disk: &DiskSpec) -> Result<DeviceId>; // hotplug
    fn remove_device(&self, vm: &VmHandle, id: &DeviceId) -> Result<()>;
}
```

`VmSpec` contains: architecture, CPUs, memory, boot (`Kernel{kernel,
initramfs, cmdline}` or `Firmware{path}`), disks (path, serial, read-only,
backing allowed), one file share (socket, tag), network (vhost-user socket),
vsock (socket path), console log path, OEM strings (SMBIOS type 11).

### 7.2 Cloud Hypervisor invocation (reference; confirm flags against the pinned version)

```text
cloud-hypervisor
  --api-socket path=<rt>/ch-api.sock
  --cpus boot=<n>
  --memory size=<m>,shared=on                 # shared memory required by vhost-user
  --balloon size=0,free_page_reporting=on
  --kernel <image>/vmlinuz --initramfs <image>/initramfs.img
  --cmdline "root=/dev/disk/by-id/virtio-root rw console=hvc0 quiet toby.machine=<id> toby.version=<ver>"
  --disk path=<root>.qcow2,image_type=qcow2,backing_files=on,serial=root
         path=<home>.qcow2,image_type=qcow2,serial=home
  --fs tag=toby,socket=<rt>/fs.sock,num_queues=1,queue_size=1024
  --net vhost_user=true,socket=<rt>/net.sock,vhost_mode=client
  --vsock cid=3,socket=<rt>/vsock.sock
  --rng src=/dev/urandom
  --console file=<rt>/console.log --serial off
  [--firmware /usr/lib/toby/firmware/hypervisor-fw]   # bootstrap builder only (no --kernel)
  [--platform oem_strings=[...]]                      # bootstrap builder only (credential injection)
```

- `<image>` is the directory of the image the root is based on; kernel
  modules come from the image itself (`/usr/lib/modules/<kver>`), because
  the kernel was installed into the image by boot adaptation.
- Guest device paths come from disk serials: `/dev/disk/by-id/virtio-root`,
  `virtio-home`, `virtio-out`, `virtio-cache`.
- Shutdown: `PUT /api/v1/vm.power-button`, wait up to 30 s for exit, then
  `PUT /api/v1/vm.shutdown`, then SIGKILL via pidfd.
- CID: always 3; hybrid vsock uses per-VM Unix sockets so CIDs never
  collide.

### 7.3 macOS engine (later; constraints to respect now)

Virtualization.framework runs the VM inside the process that creates it, so
on macOS `toby-machine` itself becomes the VMM. Disks must be raw (roots
become APFS clones; reset re-clones). File sharing uses VZ virtio-fs shares.
Keep `VmSpec` free of qcow2- and vhost-user-specific concepts at the trait
boundary (they live in the Linux implementation).

---

## 8. Machines

### 8.1 Identity and registry

- Machine ID: ULID. A machine record binds `(home, root, image, arch,
  resources, attachments, forwards, capabilities)`.
- `tobyd` resolves "machine for (home H, root R)" by scanning
  `machines/*/machine.toml`; one record per pair; created on first use.
- `toby machine ls` shows: ID, home, root, image, state, sessions,
  attachments, forwards, uptime, idle time.

### 8.2 Pairing and locking rules

- A running machine has exclusive write access to its home and root.
- Launch against (H, R) with a running machine for (H, R): join it.
- H or R is in use by a different running pair: fail with a message naming
  the running machine (`toby machine stop <id>` or choose another
  home/root). No waiting by default; `--wait` waits until it stops.
- `--ephemeral`: adds a throwaway layer over R for this machine's lifetime
  (still requires R not to be in use by another machine).

### 8.3 Lifecycle

1. `tobyd` writes `machine.toml` (generation N) and asks the ServiceManager
   to start `toby-vm@<id>` (systemd-user) or spawns the process tree
   (direct).
2. `toby-fs@<id>` and `toby-net@<id>` start first (unit dependencies), then
   cloud-hypervisor, then `toby-machine@<id>`.
3. Guest boots (§9); `toby-relay` starts and announces itself over vsock.
4. `toby-machine` runs boot-time helpers (§9.6), creates capability and
   forward listeners, applies attachments, writes `status.toml` with
   `ready = true` and `observed_generation = N`, and sends `READY=1`
   (systemd `Type=notify`).
5. Sessions start and stop.
6. **Idle stop**: when a machine has no sessions (attached or detached), no
   pinned attachments/forwards and has been idle for `idle_timeout`
   (default 15 min), `tobyd` stops it. Detached sessions count as activity.
7. Stop: `toby-machine` asks the guest to power off (power button), waits,
   then the engine escalates.

### 8.4 Desired state file (`machine.toml`)

Written only by `tobyd` (atomic write + rename). `toby-machine` watches the
directory with inotify and reconciles on every change. Contains nothing
specific to a back end.

```toml
schema = 1
generation = 42
id = "01J…"
home = "work"
root = "work"
ephemeral = false

[resources]
cpus = 4
memory = "8G"

[boot]
image = "01J…"                   # image whose vmlinuz/initramfs.img boot this machine (the root's image)

[[attach]]
id = "a1"
host = "/home/ryan/code/toby"
at = "/toby/workspace/toby"
read_only = false
pinned = false            # manual `toby attach` sets true

[[forward]]
id = "f1"
direction = "host-to-guest"   # or "guest-to-host"
host = "127.0.0.1:3000"
guest = "127.0.0.1:3000"
pinned = true

[capabilities]
sandbox_socket = "/run/toby/sandbox.sock"
models_listen = "127.0.0.1:41100"
```

### 8.5 Observed state (`status.toml`)

Written only by `toby-machine`.

```toml
observed_generation = 42
state = "ready"            # starting | ready | stopping | failed
relay_version = "1.0.0"
proto = 1
[[attach]]
id = "a1"
state = "ready"            # pending | ready | failed
error = ""
[[forward]]
id = "f1"
state = "listening"
[[session]]
id = "01J…"
argv0 = "claude"
attached = true
started = "2026-…"
```

The CLI waits for `observed_generation >= written generation` and checks the
item states before reporting success.

---

## 9. Boot path and guest

### 9.1 Image boot files

Every image carries its own stock distro kernel, installed into the image
by boot adaptation (§15.4). At build time the builder extracts two files
next to the image's disk:

- `vmlinuz`: the image's kernel (`/boot/vmlinuz-<kver>` or the distro's
  equivalent path).
- `initramfs.img`: a generic (non-host-only) dracut initramfs generated
  inside the image for that kernel, with virtio drivers (`virtio_pci
  virtio_blk virtio_net virtio_console virtiofs vmw_vsock_virtio_transport
  ext4`) and the `99toby` dracut module.

Kernel modules stay in the image (`/usr/lib/modules/<kver>`), so the running
system loads modules (overlay, netfilter, …) normally. The kernel always
matches the image's distro.

### 9.2 dracut `99toby` module

A pre-pivot hook runs in the initramfs just before switching root and
writes runtime units into `/run/systemd/system` of the real root
(`/run` is carried from the initramfs into the booted system — **VERIFY**
with the chosen distro kernel/dracut combination):

- `run-toby-fs.mount`: `What=toby`, `Type=virtiofs`, `Where=/run/toby/fs`,
  `Options=ro,nosuid,nodev` (the runtime subtree is read-only; attachments
  are bind-mounted from `/run/toby/fs/projects/<id>` with their own options).
- `toby-relay.service`: `ExecStart=/run/toby/fs/runtime/<ver>/toby guest relay`,
  where `<ver>` is read by the hook from the kernel command line
  (`toby.version=<ver>`, set by `toby internal vm`) so images never need
  rebuilding for a new Toby version; `Restart=always`,
  `After=run-toby-fs.mount`, `Requires=run-toby-fs.mount`.
- `multi-user.target.wants/toby-relay.service` symlink.

This avoids any requirement on the image's systemd version and never writes
to the root disk. The hook script is stable and version-independent; boot
adaptation installs it into the image at
`/usr/lib/dracut/modules.d/99toby/`.

### 9.3 The bootstrap builder boots differently

The one-time bootstrap builder (§15.2) boots the stock cloud image with the
bundled firmware (the cloud image's own bootloader, kernel and initramfs).
Toby's units are injected with systemd credentials in SMBIOS OEM strings
(Cloud Hypervisor `--platform oem_strings`), which requires systemd ≥ 256 in
the bootstrap image (Debian 13 has 257):

- `io.systemd.credential.binary:systemd.extra-unit.run-toby-fs.mount=<base64>`
- `io.systemd.credential.binary:systemd.extra-unit.toby-relay.service=<base64>`
- `io.systemd.credential.binary:systemd.unit-dropin.multi-user.target~toby=<base64 "[Unit]\nWants=toby-relay.service">`

**VERIFY** Cloud Hypervisor OEM strings reach systemd as credentials and
that rust-hypervisor-firmware boots the chosen cloud image.

### 9.4 Guest runtime tree (served by `toby-fs`)

```text
/run/toby/fs/                    (virtio-fs tag "toby", read-only except attachments)
  runtime/
    <ver>/toby                   the single binary, one directory per installed version
    current -> <ver>             (symlink the host flips on upgrade)
    mkosi/                       bundled mkosi (builder machines only)
    images/default/              bundled default-image mkosi configuration (builder machines only)
  projects/<attach-id>/          (passthrough to host directories)
```

Stable processes (relay, sessions) run from the exact version directory they
started with; helpers and new sessions use `current`. A version directory is
served as long as any machine still runs something from it.

### 9.5 Guest paths

| Path | Purpose |
| --- | --- |
| `/run/toby/fs` | virtio-fs mount |
| `/run/toby/sessions/<id>/` | per-session socket, spec, exit record (root-only) |
| `/run/toby/sandbox.sock` | capability: Toby sandbox API (used by `toby-connect`) |
| `127.0.0.1:41100` | capability: models + remote MCP proxy (port configurable) |
| `/home/<user>` | home disk |
| `/toby/workspace/<name>` | default attachment target |
| `/run/toby/bin/` | symlinks created at boot: `toby` → `/run/toby/fs/runtime/current/toby`, plus multi-call names `toby-connect`, `toby-session`, `toby-helper` → `toby`. Nothing is installed into the root. |

### 9.6 Boot-time helpers (run by `toby-machine` after relay hello)

All run as `Root` through the relay, in order, idempotent:

1. `toby-helper net-up --addr … --gw … --dns …`: configures `eth0`, default
   route and `lo` via netlink; writes `/run/toby/resolv.conf` and
   bind-mounts it over `/etc/resolv.conf` (handles a symlinked
   `/etc/resolv.conf`). Sets the hostname to the home name.
2. `toby-helper user-setup --name <u> --uid <n> --shell <sh> --sudo=<bool>`:
   ensures the group and user exist in the root by editing
   `/etc/passwd`, `/etc/group` and `/etc/shadow` directly (no `useradd`
   dependency); if the name or UID is taken by a different image user,
   that entry is left alone and Toby's entry wins for the home's UID. The
   shell is the home's configured shell if present in the image, else
   `/bin/bash`, else `/bin/sh`. Passwordless sudo via
   `/etc/sudoers.d/toby` (default on; `home.sudo = false` disables).
3. `toby-helper home-mount --device /dev/disk/by-id/virtio-home --at /home/<u>`:
   mounts the (already formatted) home with the `mount(2)` syscall (no
   `util-linux` dependency); on first use chowns the root of the home and
   copies `/etc/skel`.
4. `toby-helper links`: creates the `/run/toby/bin/` symlinks (§9.5).
5. Attachments and listeners from desired state (§10, §11).

---

## 10. File sharing (`toby-fs`, `toby-vfs`)

One virtio-fs device per machine, served over vhost-user by `toby-fs@<id>`,
built on `vhost-user-backend`, `virtio-queue`, `vm-memory` and
`fuse-backend-rs` (with its `Vfs` layer to combine several back-end
filesystems under one device).

### 10.1 Tree

- `/` synthetic, read-only.
- `/runtime` read-only view of the installed runtime directory
  (`/usr/lib/toby/guest/<arch>/…`).
- `/projects/<attach-id>` passthrough file systems added and removed at
  runtime.

### 10.2 Control API (stable, unix socket, framed CBOR)

`Add{id, host_path, read_only}`, `Remove{id}`, `List`. `toby-fs` opens the
host path once as an `O_PATH` directory descriptor and roots the
passthrough there. All lookups stay beneath that descriptor
(`openat2` with `RESOLVE_BENEATH | RESOLVE_NO_MAGICLINKS`); symlinks are
resolved inside the guest, never followed out of the attachment on the
host.

### 10.3 Identity mapping

- Host files owned by the host user are reported with the guest user's UID
  and GID. Everything else is reported as the overflow ID (65534).
- Every guest write, from any guest identity including root, is performed
  as the host user (the process runs as the host user; no identity
  switching).
- `chown`/`chgrp` to the guest user or root: success, no-op. Anything else:
  `EPERM`.
- setuid/setgid bits are stripped on create and chmod; device nodes and
  FIFOs are refused.
- The guest mounts attachments with `nosuid,nodev`.

### 10.4 Attach flow

1. `tobyd` adds `[[attach]]` to desired state.
2. `toby-machine` calls `toby-fs` `Add`.
3. `toby-machine` runs `toby-helper attach --src /run/toby/fs/projects/<id>
   --at <target> --ro=<bool>` as root (bind mount with `nosuid,nodev`,
   read-only remount when requested; creates the mount point).
4. Detach: helper unmounts (refuses while in use unless forced), then
   `toby-fs` `Remove`.
5. Attachment reference counting lives in `tobyd`: session-scoped
   attachments are removed when the last session using them ends; pinned
   attachments stay until `toby detach` or machine stop.
6. Mount points in `$HOME` or the root that correspond to removed
   attachments are left as empty directories owned by root with mode 0555
   so writes fail instead of landing in the root.

### 10.5 Risks

- **VERIFY** fuse-backend-rs passthrough works fully unprivileged
  (file handles, `CAP_DAC_READ_SEARCH`, `O_PATH` behavior). Fallback: run
  one upstream `virtiofsd` per attachment with `--translate-uid` /
  `--translate-gid` and Cloud Hypervisor hotplug of fs devices; this changes
  only `toby-fs`/`toby-machine`, not the rest of the design.
- Performance on large repositories (`git status`, `npm install`):
  benchmark cache modes in milestone 4.

---

## 11. Networking, vsock, forwards

### 11.1 Egress

`toby-net@<id>` runs `passt --vhost-user` on `<rt>/net.sock` with fixed
guest addressing (`10.0.2.15/24`, gateway `10.0.2.2`, DNS forwarder
`10.0.2.3`; configurable), no inbound port forwarding. Cloud Hypervisor
connects as a vhost-user net client.

Fallback if vhost-user passt is not usable (**VERIFY**): run
cloud-hypervisor inside a pasta-created user+network namespace with a tap
device. Only `toby-net`/`toby-vm` units change.

### 11.2 vsock transport (Cloud Hypervisor hybrid vsock)

- Host→guest: connect to `<rt>/vsock.sock`, write `CONNECT 1024\n`, read
  `OK <port>\n`, then the stream is connected to the guest listener on
  vsock port 1024.
- Guest→host: guest connects to CID 2 port 1024; Cloud Hypervisor connects
  to `<rt>/vsock.sock_1024`, which `toby-machine` listens on.
- `toby-relay` accepts host-initiated connections on guest vsock port 1024
  and **rejects any peer whose CID is not 2 (host)**, so guest processes
  cannot reach the relay through vsock loopback.

### 11.3 Stream header protocol (`toby-proto`, version 1)

Every vsock connection starts with one header frame; after the
acknowledgement the connection is raw bytes (or a framed protocol owned by
the two endpoints, e.g. the session protocol), and stable processes only
splice.

Frame: `u32 big-endian length | u8 type | CBOR payload`. Maximum header
size 64 KiB. Unknown fields ignored; unknown types rejected.

Host→guest (to relay, port 1024):

| Header | Meaning |
| --- | --- |
| `Control{proto_versions}` | Relay control channel; request/response frames follow (§11.4) |
| `SessionAttach{session_id}` | Relay bridges to `/run/toby/sessions/<id>/sock`; then end-to-end session protocol |
| `Dial{target}` | Relay connects to `tcp:127.0.0.1:<port>` or `unix:<path>` and splices (host→guest forwards) |

Guest→host (to `toby-machine`, `vsock.sock_1024`):

| Header | Meaning |
| --- | --- |
| `RelayHello{version, proto_versions, boot_id}` | Relay started; `toby-machine` runs boot helpers / re-registers listeners |
| `Accepted{listener_id}` | A guest listener accepted a connection; `toby-machine` connects it to the target registered for that listener |

Trust model: everything inside a VM is one trust domain (the user may have
root there). `toby-machine` treats all guest-originated bytes as untrusted:
it only honors `listener_id`s it registered for this machine and routes each
to exactly one pre-configured target. Header parsing is fuzzed.

### 11.4 Relay control requests (host → relay)

| Request | Result |
| --- | --- |
| `Hello{versions}` | chosen version, relay version, boot id |
| `Spawn{session_id, argv, env, cwd, identity, tty: {rows, cols} or none, keep_after_exit}` | runs `systemd-run --scope --collect --unit=toby-s-<id> -- /run/toby/fs/runtime/<ver>/toby guest session …` (`<ver>` = `runtime/current` at spawn time, resolved to a fixed version so the session keeps it); returns once the session socket exists |
| `Listen{listener_id, bind}` | bind `tcp:127.0.0.1:<port>` or `unix:<path>` (mode, owner) in the guest |
| `Unlisten{listener_id}` | close listener |
| `Sessions` | list live sessions and exit records |
| `Ping` | liveness |

Spawn uses `systemd-run --scope` so sessions live in their own cgroup and
survive relay restarts; `toby-session` drops to the identity itself.

### 11.5 Forwards

- Host→guest: `toby-machine` listens on the host address; each accepted
  connection opens a `Dial` stream to the guest target.
- Guest→host: relay `Listen`s on the guest address; each accepted guest
  connection arrives as `Accepted{listener_id}`; `toby-machine` connects to
  the host target.
- Conflicts on host ports across machines are rejected by `tobyd` with an
  error naming the owner.
- Optional later: automatic host→guest forwarding driven by
  `toby-helper ports` (reads `/proc/net/tcp*`).

### 11.6 Capabilities

Created at machine start from desired state:

| Guest end | Host target | Purpose |
| --- | --- | --- |
| `unix:/run/toby/sandbox.sock` (mode 0666) | `<rt>/capability.sock` (tobyd) | Toby sandbox API: MCP connect routing, Toby MCP, session info |
| `tcp:127.0.0.1:41100` | `<rt>/proxy.sock` (toby-proxy) | models proxy and remote MCP proxy |

`toby-machine` prepends a small header with the machine ID when connecting
to host services. Host services accept connections only from their own UID
(`SO_PEERCRED`).

---

## 12. Host supervision back ends (`toby-svc`)

### 12.1 Interface

```rust
trait ServiceManager {
    fn ensure_daemon(&self) -> Result<()>;
    fn start_machine(&self, id: &MachineId) -> Result<()>;
    fn stop_machine(&self, id: &MachineId) -> Result<()>;
    fn list_machines(&self) -> Result<Vec<(MachineId, UnitState)>>;
    fn restart(&self, unit: UnitName) -> Result<()>;   // tobyd, proxy, machine parts
    fn logs(&self, unit: UnitName, follow: bool) -> Result<LogStream>;
    fn events(&self) -> Result<EventStream>;
}
```

Selection: `daemon.backend = "systemd-user"` (default) or `"direct"`. No
automatic fallback. If the systemd user instance is unreachable in
`systemd-user` mode the CLI errors with the reason and the fix (switch to
`direct`). Switching back end requires stopping all machines first.

### 12.2 systemd-user back end

Unit files shipped in `/usr/lib/systemd/user/`; `tobyd.socket` enabled
globally by the package (`systemctl --global enable tobyd.socket`). `tobyd`
talks to the user manager over D-Bus (`zbus`); logs via `journalctl --user`
subprocess.

```ini
# tobyd.socket
[Socket]
ListenStream=%t/toby/tobyd.sock
SocketMode=0600
DirectoryMode=0700
[Install]
WantedBy=sockets.target

# tobyd.service
[Service]
Type=notify
ExecStart=/usr/lib/toby/current/toby daemon
Restart=on-failure

# toby-proxy.socket / toby-proxy.service  (same pattern, %t/toby/proxy.sock,
#   ExecStart=/usr/lib/toby/current/toby internal proxy)

# toby-fs@.service
[Unit]
StopWhenUnneeded=yes
[Service]
Type=notify
ExecStart=/usr/lib/toby/current/toby internal fs --machine %i

# toby-net@.service
[Unit]
StopWhenUnneeded=yes
[Service]
Type=exec
ExecStart=/usr/lib/toby/current/toby internal net --machine %i   # reads machine.toml, execs passt

# toby-vm@.service
[Unit]
BindsTo=toby-fs@%i.service toby-net@%i.service
After=toby-fs@%i.service toby-net@%i.service
Wants=toby-machine@%i.service
[Service]
Type=exec                       # readiness is reported by toby-machine@
ExecStart=/usr/lib/toby/current/toby internal vm --machine %i    # reads machine.toml, execs cloud-hypervisor
KillMode=mixed
TimeoutStopSec=45

# toby-machine@.service
[Unit]
PartOf=toby-vm@%i.service
After=toby-vm@%i.service
[Service]
Type=notify
ExecStart=/usr/lib/toby/current/toby internal machine --machine %i
Restart=on-failure
```

`/usr/lib/toby/current` resolves when a unit starts, so already-running
units keep their version and newly started ones use the new one. The
`internal vm` and `internal net` launchers only read desired state and
`exec` the real program.

Linger warning:

- In `systemd-user` mode, when the CLI starts a machine or a session and
  logind's `Linger` property for the user is false, print once per `tobyd`
  lifetime:
  ```text
  warning[daemon.linger-disabled]: linger is off; machines and sessions stop shortly after your last login session ends.
           enable: loginctl enable-linger   ·   silence: add "daemon.linger-disabled" to settings.suppress_warnings
  ```
- Silence through the general warning mechanism:
  `settings.suppress_warnings = ["daemon.linger-disabled"]` (§14.6).
- `toby linger on|off` wraps logind `SetUserLinger`; when polkit refuses,
  print the `loginctl` command to run with admin rights.

### 12.3 direct back end

- The CLI starts `tobyd` detached (`setsid`, double fork) if its socket does
  not answer.
- `tobyd` starts one `toby internal machine --supervise --machine <id>` per
  machine, fully detached, so machines survive `tobyd` restarts.
- In `--supervise` mode it starts and supervises `toby internal fs`, passt
  and cloud-hypervisor (pidfd), reproducing the unit relationships: fs or
  net exit → stop VM; VM exit → stop the rest.
- Logs go to `<state>/logs/<unit>.log` with size-based rotation.
- No linger warning. `toby daemon status` notes that processes survive
  logout only when logind `KillUserProcesses=no`.

---

## 13. Sessions and the terminal

### 13.1 Session protocol (CLI ↔ `toby-session`, end-to-end, version 1)

Framed CBOR, same frame format as §11.3. The stable middle (`toby-machine`,
`toby-relay`) never parses it.

| Frame | Direction | Meaning |
| --- | --- | --- |
| `Hello{versions, rows, cols, want_replay}` | client → session | attach |
| `Welcome{version, state}` | session → client | accepted |
| `Replay{bytes}` | session → client | recent output from the ring buffer |
| `Stdin{bytes}` / `CloseStdin` | client → session | input |
| `Stdout{bytes}` / `Stderr{bytes}` | session → client | output (stderr only without a tty) |
| `Resize{rows, cols}` | client → session | window size |
| `Signal{n}` | client → session | deliver to the session's process group |
| `Exit{code or signal}` | session → client | process ended |
| `Detached{reason}` | session → client | another client attached |

### 13.2 `toby-session` behavior

- Creates the PTY (or pipes), forks the child with the requested identity
  (`setgroups`, `setgid`, `setuid`; `Root` keeps root), environment, cwd and
  a new session / controlling terminal.
- Holds the PTY master; keeps a ring buffer of recent output (1 MiB).
- Listens on `/run/toby/sessions/<id>/sock`; one attached client at a time;
  the newest attach wins and the previous client gets `Detached`.
- On child exit writes `/run/toby/sessions/<id>/exit` and keeps the socket
  until the exit is collected or 1 h passes (`keep_after_exit`).

### 13.3 Attach path

CLI → `POST /v1/sessions` on `tobyd` → `tobyd` asks `toby-machine` to spawn
→ response contains the machine's `session.sock` path and session ID → CLI
connects to `session.sock`, sends `SessionAttach{session_id}` (header
consumed by `toby-machine`), which opens a vsock `SessionAttach` to the
relay, which bridges to the session socket → end-to-end session protocol.

### 13.4 Detach and reattach

- Closing the terminal or losing the connection detaches; the session keeps
  running.
- `toby sessions ls`, `toby attach [<session>]`, `toby sessions kill <id>`.
- Detach key: a configurable prefix (default `Ctrl-\` then `d`), active only
  while attached through the CLI.
- Reattach: CLI sends `Hello{want_replay=true}`, prints the replay, then
  sends `Resize` with a transient size change to force full-screen tools to
  redraw.
- Relaunching `toby <tool> --home H` when a detached session of that tool
  exists: prompt to attach or start new; `--attach` / `--new` skip the
  prompt.

### 13.5 Terminal handling now vs later

Now (milestones 2–8): the CLI puts the terminal in raw mode, forwards
bytes, SIGWINCH → `Resize`, and restores the terminal on exit or panic. No
emulation.

Later (milestone 9, part of this implementation): the CLI becomes a small
compositor:

- Parses the session output with a terminal emulator library
  (`alacritty_terminal` or `wezterm-term`; choose by support for mouse,
  bracketed paste, alternate screen, kitty keyboard protocol, OSC 52,
  OSC 8 hyperlinks, synchronized output, true color).
- Renders session grid + status bar (tmux-like: machine, MCP status,
  forwards, pending approvals) + floating windows (herdr-style) to the real
  terminal with diffed updates.
- Session PTY size = terminal size minus the status bar.
- **Overlay approvals are the default** approval UI; CLI approvals
  (`toby approvals`) and web approvals remain available.
- Events come from `GET /v1/events` (WebSocket).

---

## 14. Configuration

### 14.1 Global `~/.config/toby/config.toml`

```toml
[daemon]
backend = "systemd-user"         # or "direct"
idle_timeout = "15m"

[paths]
storage_root = "~/.local/share/toby"
state_root = "~/.local/state/toby"

[settings]
suppress_warnings = []           # registered warning IDs, or ["*"]; e.g. "daemon.linger-disabled"
yolo = false
projects_dir = "~/Projects"      # base for relative project paths
allow_external_projects = false
autoload_project_config = false

[defaults]
home = "default"
image = "default"                # the bundled default image (§15.1); or { mkosi = … } / { dockerfile = …, context = … } / { registry = … } / { archive = … }
# cpus / memory / disk sizes: see §14.5 for computed defaults

instructions = ["~/AGENTS.md", "~/instructions/*.md"]

[permissions.paths]               # guest paths passed to tool permission settings
"~/notes" = "allow"

[permissions.actions]
"git.commit" = "ask"
"git.push" = "always-ask"

[models.anthropic]
protocol = "anthropic"
name = "Anthropic"
url = "https://api.anthropic.com"
headers = { "x-api-key" = "{file:~/.config/toby/keys/anthropic}" }

[mcp.github]
kind = "stdio"
command = ["npx", "-y", "@modelcontextprotocol/server-github"]
env = { GITHUB_TOKEN = "{file:~/.config/toby/keys/github}" }
placement = "isolated"           # or "machine"

[mcp.docs]
kind = "http"
url = "https://example.com/mcp"
headers = { Authorization = "Bearer {env:DOCS_TOKEN}" }

[tools.claude]
models = "anthropic"
mcp = ["github", "docs", "toby"]
params = []
```

### 14.2 Secrets: no secret store

Toby stores no secrets of its own. The secrets that exist are:

| Secret | Where it lives |
| --- | --- |
| Model provider credentials (API keys, auth headers) | referenced from config with substitutions; used only by `toby-proxy` / `tobyd` |
| MCP credentials (remote headers, stdio `env`) | referenced with substitutions; remote ones used only by `toby-proxy`; isolated stdio ones placed only in that MCP process's environment in its services machine |
| Registry credentials for private image pulls | an auth file referenced with `{file:}`, attached to the builder for that build only |
| Git credentials and signing keys | the host's normal git setup; used only by Toby MCP host actions on the host |
| Tool logins (Claude/Codex OAuth tokens, etc.) | created by the tools themselves inside the home disk; Toby never handles them |

Substitutions (carried over from current Toby) work in model headers and
MCP URLs, headers, command arguments and environment values:

- `{file:path}`: trimmed file contents (relative to the config directory,
  `~` allowed). **Recommended**, because the daemon can re-read it at any
  time, including after a restart.
- `{env:NAME}`: an environment variable of the `tobyd` process. In the
  systemd-user back end that is the user manager's environment
  (`~/.config/environment.d/*.conf` or `systemctl --user
  import-environment`); in the direct back end it is the environment of the
  CLI that started `tobyd`. `toby doctor` reports unresolved references.

Substitutions are resolved in host processes when used, never written to
disk by Toby, and never sent to guests except the isolated MCP case above.
Project config cannot use substitutions.

### 14.3 Project `.toby/config.toml` (optional, committed by users)

```toml
home = "work"                    # default home for this project
root = "work"                    # default root
image = { dockerfile = ".toby/Dockerfile", context = "." }
forwards = [{ direction = "host-to-guest", host = 3000, guest = 3000 }]
mcp = ["github"]                 # enables configured servers by name; cannot define credentials

[projects.app]                   # optional extra projects attached with this one
path = "."
primary = true
[projects.library]
path = "../library"

workdir = "/toby/workspace/app"
```

Precedence: CLI flags > project config > global config > built-in
defaults. Project config may not use substitutions or reference host paths
outside `projects_dir` unless `allow_external_projects` is set. Project
config is loaded only when `settings.autoload_project_config = true`
(default false, carried over), because a cloned repository could otherwise
enable configured MCP servers or forwards; when a project config exists but
is not loaded, Toby emits `project.autoload-disabled`.

Config format is TOML throughout (pre-1.0 format change from YAML; no
compatibility with the old files). Unknown fields are errors.

### 14.4 Home and root records

```toml
# <state>/homes/work.toml
name = "work"
username = "ryan"
uid = 1000
sudo = true
default_root = "work"

# <state>/roots/work.toml
name = "work"
image = "01J…"
source = { dockerfile = "/home/ryan/code/toby/.toby/Dockerfile" }
```

### 14.5 Resource defaults (overridable globally, per project, per home)

| Setting | Default |
| --- | --- |
| Tool machine CPUs | half the host's CPUs, at least 2, at most 8 |
| Tool machine memory | half of host RAM, at most 8 GiB (balloon free-page reporting returns unused memory) |
| Services machine (per isolated MCP) | 1 CPU, 1 GiB |
| Builder machine | half the host's CPUs, memory as tool machines |
| Image disk (virtual, sparse) | 64 GiB |
| Home disk (virtual, sparse) | 100 GiB |
| Build cache disk (virtual, sparse) | 100 GiB |
| Idle timeout: tool machines | 15 min |
| Idle timeout: services machines | 5 min |
| Builder machines | stop when their job ends |
| Session exit record kept | 1 h |
| Session replay buffer | 1 MiB |

### 14.6 Behavior carried over from current Toby

These existing features keep their meaning in the new design:

- **Instructions**: `instructions` (paths/globs on the host) are read at
  launch and written into each tool's native instruction file in the home
  (e.g. `~/.config/claude/CLAUDE.md`, OpenCode `AGENTS.md`); missing
  entries warn with `config.instruction-missing`.
- **Permissions**: `permissions.paths` (guest paths, `allow`/`deny`) are
  written into tool permission settings; Toby adds `/tmp` by default;
  `--yolo`/`settings.yolo` adds `/` and enables each tool's permission-bypass
  flag. `permissions.actions` (`allow`, `deny`, `ask`, `always-ask`) govern
  Toby MCP host actions; `always-ask` still asks under yolo.
- **Projects**: one or more projects per launch, one primary; relative paths
  resolve from `settings.projects_dir`; paths outside it require
  `allow_external_projects`; each is attached at
  `/toby/workspace/<name>`; `workdir` defaults to the primary project.
- **Tools**: the full current catalog becomes built-in manifests (§16.1):
  `opencode`, `claude`, `codex`, `copilot`, `cursor`, `dcode`, `grok`,
  `speckit`, `t3`, `emdash`, `npm`, `uv`, `github_cli`, `gitlab_cli`, `fj`,
  and `exec` (run an arbitrary command). `docker` is dropped. Tool
  dependencies are ordered topologically; `params` pass extra arguments;
  `--install` installs and exits; `--upgrade` forces the installer.
- **Models**: discovery of the provider's model list is cached for 5
  minutes per provider and written into tool configs; unreachable providers
  warn with `models.endpoint-unavailable` and are skipped.
- **Warnings**: registered warning IDs, printed as `warning[<id>]: …`,
  suppressible with `settings.suppress_warnings` (IDs or `"*"`).
- **Launch files**: a named launch file (`toby run -f review.toml`) can
  set image, home/root, projects, tools, params, workdir and settings for
  one launch, replacing the old launch YAML.

---

## 15. Images and builds

Toby does not publish prebuilt images. Every image is built locally inside a
builder machine and ends in the same layout (§6.1: `disk.qcow2`, `vmlinuz`,
`initramfs.img`), so roots, homes and machines never care which source
produced an image.

### 15.1 Image sources and the default image

Four source kinds, usable for user images (global/project/launch config)
and MCP images (`[mcp.<name>].image`) alike:

| Source | Config | Built with |
| --- | --- | --- |
| mkosi configuration directory | `image = { mkosi = "path/to/dir" }` | bundled mkosi, `Format=directory` |
| Dockerfile | `image = { dockerfile = "…", context = "." }` | buildah |
| Registry reference | `image = { registry = "ghcr.io/…@sha256:…" }` | buildah pull |
| OCI archive | `image = { archive = "path.tar" }` | buildah pull |

- Every source produces a **root filesystem tree**; boot adaptation
  (§15.4) and export (§15.3) are then identical for all of them.
- Users keep their existing Dockerfiles and registry images (MCP servers are
  often published as container images). mkosi is the native choice for new
  images that should be proper OS images.
- **Default image**: built from an **mkosi configuration bundled with
  Toby** at `/usr/share/toby/images/default/` (arch-independent, so
  `/usr/share`):
  - Debian 13 (trixie), with kernel (`linux-image-cloud-<arch>`), systemd,
    dracut, sudo, ca-certificates, curl, git, bash, tar, unzip, zstd, jq,
    make, openssh-client, python3, and Node.js LTS (from the NodeSource apt
    repository configured through mkosi's sandbox tree, since Debian's own
    Node.js is older than the tools need);
  - plus what builders need: buildah, podman, e2fsprogs, bubblewrap and
    mkosi's other runtime dependencies (mkosi itself comes from the runtime
    tree, below).
  - Built the first time it is needed and rebuilt when the bundled
    configuration or the bundled mkosi version changes (content hash).
- The same default image boots **builder machines**, so there is one image
  to maintain.
- **mkosi is bundled**: a pinned mkosi release (Python; LGPL-2.1-or-later,
  shipped as source with its license) installed at `/usr/share/toby/mkosi/`
  and served read-only to builder machines through the runtime tree
  (`/run/toby/fs/runtime/mkosi/`), so every build uses the version Toby was
  tested with. Pinned in `packaging/bundled.toml` like Cloud Hypervisor.
- Architecture is part of every image's identity.

### 15.2 Bootstrap (once per architecture)

Building the default image needs a builder, and builders boot the default
image, so the first build needs a different starting point:

1. Download the current Debian 13 (trixie) `genericcloud` qcow2 for the
   host architecture (EFI bootable, systemd 257). Toby pins the distro
   release only, not a specific build: it uses Debian's `latest` build for
   trixie and checks it against the `SHA512SUMS` file Debian publishes next
   to it (fetched over HTTPS). This is the only automatic download in Toby,
   happens once per architecture, and can be replaced by `toby builder
   bootstrap --base <file>` for offline hosts (the file must be a Debian 13
   cloud image; no hash is required).
2. Create a throwaway overlay over it and boot the **bootstrap builder**
   with the bundled firmware and credential injection (§9.3).
3. Provision as root: `apt-get update && apt-get full-upgrade`, then
   install the default image's builder dependencies from Debian
   (`apt-get install python3 bubblewrap buildah podman
   e2fsprogs dracut` plus mkosi's other runtime dependencies for Debian
   targets).
4. Build the default image in it with the bundled mkosi and the bundled
   configuration (§15.3, §15.4). Power off.
5. From now on builder machines boot the default image. The bootstrap cloud
   image is no longer needed; `toby builder bootstrap --clean` deletes it.
   Rebuilding the default image later uses the existing default image as
   the builder.

### 15.3 Build steps

1. `tobyd` creates `images/<new>/disk.qcow2` (sparse, default 64 GiB) and
   starts a builder machine: the default image with an ephemeral layer,
   `cache.qcow2` (serial `cache`; holds `/var/lib/containers` and mkosi's
   package cache, incremental cache and tools tree), the output disk
   (serial `out`), the build context attached read-only at
   `/build/context`, and the new image directory `images/<new>.tmp/`
   attached writable at `/build/boot`.
2. Produce the root tree (as root, streamed to the build log):
   - mkosi: `python3 /run/toby/fs/runtime/mkosi/bin/mkosi
     -C /build/context --format=directory --architecture=<arch>
     --output-directory=/build/tree --incremental=yes
     --package-cache-directory=/cache/mkosi/packages
     --cache-directory=/cache/mkosi/cache --tools-tree=default build`
     (the tools tree provides the package manager for non-Debian targets;
     it is cached on the cache disk). The tree is the configured output
     under `/build/tree`. Confirm option names against the pinned mkosi
     release; Toby's overrides always win over the user's `mkosi.conf` for
     format, architecture, output and cache locations.
   - Dockerfile: `buildah build --layers -f <file> -t toby/build
     /build/context`, then `ctr=$(buildah from toby/build)` and
     `tree=$(buildah mount $ctr)`.
   - Registry: `buildah pull <ref>` then as above (registry credentials: an
     auth file given with a `{file:…}` substitution is attached read-only
     for this build only).
   - OCI archive: `buildah pull oci-archive:/build/context/<file>` then as
     above.
3. Boot adaptation (§15.4) on the tree, run with `chroot` into it (or
   `buildah run` for container sources).
4. Export: `mkfs.ext4 -L toby-root /dev/disk/by-id/virtio-out`, mount at
   `/out`, `cp -a --sparse=always "$tree"/. /out/`, copy the kernel and
   generated initramfs to `/build/boot/` (`vmlinuz`, `initramfs.img`),
   capture the image config (container sources: `buildah inspect --type
   image`; mkosi: empty config plus `os-release`) into the image record,
   `fstrim /out`, unmount.
5. Power off; move `images/<new>.tmp/` into place; mark files read-only;
   write the image record (source, arch, kernel version, adaptation
   version, config).
6. Build logs stream to the CLI (`WebSocket /v1/builds/<id>/logs`).

The same builder machinery formats new home disks (`mkfs.ext4 -L
toby-home`, §6.2).

### 15.4 Boot adaptation

Runs inside the root tree as root (for every source kind; idempotent, so
mkosi configurations that already include the kernel, dracut and systemd
only get the hook and initramfs). Detects the distro from
`/etc/os-release` (`ID`, `ID_LIKE`) and uses its package manager for
anything missing:

| Family | Packages installed if missing |
| --- | --- |
| Debian/Ubuntu (`apt`) | `systemd systemd-sysv sudo dracut ca-certificates` + kernel `linux-image-cloud-<arch>` (Debian) or `linux-kvm` / `linux-virtual` (Ubuntu) |
| Fedora/RHEL (`dnf`) | `systemd sudo dracut ca-certificates kernel-core` |
| Arch (`pacman`) | `systemd sudo dracut ca-certificates linux` |
| openSUSE (`zypper`) | `systemd sudo dracut ca-certificates kernel-default-base` |

Then:

1. Install the `99toby` dracut module into
   `/usr/lib/dracut/modules.d/99toby/` (copied from the runtime tree).
2. Generate the initramfs:
   `dracut --no-hostonly --force --kver <kver> --add toby --add-drivers "…virtio list…" /boot/toby-initramfs.img`.
3. Make `/sbin/init` resolve to systemd; mask units that make no sense in a
   Toby machine (`getty@tty1`, `serial-getty@hvc0`, `systemd-firstboot`,
   network managers that would fight `net-up`); empty `/etc/machine-id` so
   each root gets its own on first boot.
4. Record the adaptation version in `/usr/lib/toby-adaptation` (and in the
   image record) so Toby knows when an image needs rebuilding after an
   adaptation change.

Unsupported distros (no systemd available, e.g. Alpine) fail with a clear
message and a pointer to the custom image requirements (§15.5).

### 15.5 Custom image requirements (to be documented for users)

An image works with Toby if, after boot adaptation:

- systemd is `/sbin/init`;
- a Linux kernel with its modules is installed, plus dracut (adaptation
  installs both on supported distros);
- the architecture matches the host;
- the tools a user wants to run have their prerequisites in the image (each
  tool manifest lists `requires`, e.g. `curl`, `git`, `node`/`npm`, `bash`).

For mkosi sources, the configuration should include the kernel, systemd
and dracut packages itself (adaptation then only adds the hook and
initramfs); any `Format=` in the user's configuration is overridden with
`directory`. Container images for unsupported distros must provide systemd,
a kernel and dracut themselves and set the label `dev.toby.adapted=manual`
to skip automatic package installation. Toby needs nothing else from an image: user setup, mounts and
networking are done by Toby's own static binary.

### 15.6 Preparing images: `toby image prepare`

One command builds every image Toby will need, so the first launch does not
stall on builds and so images can be refreshed deliberately:

```text
toby image prepare [--all] [--default] [--mcp [<name>…]] [--project [PATH]] [--rebuild] [--pull]
```

- No flags: prepare everything referenced by the current configuration
  (equivalent to `--default --mcp --project` for the current directory's
  project config when loaded).
- `--default`: the default image (which also boots builders and formats
  homes). Runs the one-time bootstrap (§15.2) first if needed.
- `--mcp`: the image of every isolated MCP server (`[mcp.<name>].image`,
  or the default image), or only the named ones.
- `--project`: the image configured for a project (global default,
  project config or launch file).
- `--all`: all of the above for every configured MCP server and every root's
  recorded source.
- Up-to-date images are skipped: an image is current when its source hash
  (mkosi configuration directory hash + bundled mkosi version, Dockerfile +
  context content hash, registry digest, or archive hash) and the boot
  adaptation version match the image record. `--rebuild` forces a
  rebuild; `--pull` re-resolves registry references and base images.
- Builds run one after another through the builder machine with shared
  build cache; progress and logs stream like single builds.
- Existing roots keep their current image; `toby root ls` shows which roots
  are behind and `toby root rebase` moves them.

API: `POST /v1/images/prepare` with the same options, returning build IDs.

---

## 16. Tools, models, MCP and host actions

### 16.1 Tool manifests (`toby-tools`)

Built-in manifests are embedded in `tobyd`; `~/.config/toby/tools/*.toml`
override or add tools.

```toml
[tool]
name = "claude"
requires = ["bash", "curl", "git"]          # checked in the root (command -v)
check = ["claude", "--version"]
install = { script = "curl -fsSL https://claude.ai/install.sh | bash" }  # runs as User
update = { script = "claude update" }
launch = ["claude"]
env = { ANTHROPIC_BASE_URL = "{{ models.url }}/anthropic", ANTHROPIC_AUTH_TOKEN = "{{ models.token }}" }

[[tool.files]]
path = "~/.claude/settings.json"
format = "json"
mode = "merge"                               # merge | replace
template = """{ "env": { "ANTHROPIC_BASE_URL": "{{ models.url }}/anthropic" } }"""

[tool.mcp]
path = "~/.claude.json"
format = "json"
pointer = "/mcpServers"                      # where entries are written
entry = """{ "command": "{{ connect }}", "args": ["mcp/{{ name }}"] }"""

[[tool.forwards]]
when = "login"
direction = "host-to-guest"
port = 54545
```

Execution (by `tobyd`, only through relay spawn and helpers, one tool
operation at a time per machine):

1. `requires` check; fail with a clear message naming the missing commands
   (system dependencies belong in the image).
2. `check`; on failure run `install` as `User`, streaming progress.
3. Write/patch files with `toby-helper patch-file` (atomic, as `User`, in
   `$HOME`). Generated values are stable per machine (§16.2), so rewriting
   is idempotent.
4. Spawn `launch` as a session with a TTY in the chosen attachment's
   directory.

Templating: `minijinja`. Context: `models.url`, `models.token`, `mcp[]`,
`connect` (`/run/toby/bin/toby-connect`, a multi-call symlink to the
`toby` binary), `user`, `home`, `workspace`.

### 16.2 Models proxy (`toby-proxy`)

- Guest sees `http://127.0.0.1:41100/<provider>/…` and a synthetic token
  `toby_<machine-id>_<random>` stored in the machine's state.
- `toby-proxy` receives the connection through `toby-machine` with the
  machine ID header, checks the synthetic token for that machine, replaces
  auth headers with the configured secret headers, and proxies to the
  provider (`hyper`, rustls). Streaming responses are passed through.
- Model discovery (listing available models to write into tool configs) is
  done by `tobyd` through the same provider config.

### 16.3 MCP

| Kind / placement | Guest config entry | Where it runs | Secrets |
| --- | --- | --- | --- |
| `http` | URL `http://127.0.0.1:41100/mcp/<name>` | upstream; `toby-proxy` adds headers | host only |
| `stdio`, `placement = "machine"` | the real command | tool's machine, as `User` | none allowed (config error if `env` uses secrets) |
| `stdio`, `placement = "isolated"` (default when secrets are used) | `toby-connect mcp/<name>` | services machine, fresh `toby-session` per connection | only in that process's environment |
| built-in `toby` | `toby-connect mcp/toby` | `tobyd` | host |

Isolated flow: `toby-connect` connects stdin/stdout to
`/run/toby/sandbox.sock` with a request `{mcp: name}` → `toby-machine`
→ `tobyd` capability endpoint → `tobyd` ensures **that MCP server's own
services machine** is running (one services machine per isolated MCP
server; image from `[mcp.<name>].image` or the default image; its own
persistent root and a small persistent home per MCP server so installs and
caches survive restarts) → spawns the MCP command in a fresh session there
with its credentials in the process environment → `tobyd` returns an
endpoint and the two `toby-machine`s splice the streams (tool machine ↔
services machine) without `tobyd` in the data path. Services machines
idle-stop after 5 minutes without connections.

Local HTTP MCP servers (current `type: local, transport: http`) run the
same way in their services machine; `toby-proxy` routes
`127.0.0.1:41100/mcp/<name>` to the server's endpoint inside that machine
through a forward. Network access for services machines is outbound only
(passt), with optional explicit guest→host forwards
(`[mcp.<name>].host_ports`) replacing the old `network: host`.

### 16.4 Toby MCP and host actions

Built-in MCP server in `tobyd`, reached with `toby-connect mcp/toby`:

- `git_status`, `git_commit`, `git_fetch`, `git_push`, `git_rebase`,
  `git_tag`: run on the host with host credentials in the host path of the
  attachment that contains the guest working directory (mapping guest path
  → attachment → host path; refuse paths outside attachments).
- `forward_request{port, direction}`: asks the user to approve a new
  forward.
- `session_info`.

Actions that need approval create an approval record and block until
decided or timed out.

### 16.5 Approvals

- Record: `{id, created, machine, session, kind, summary, detail,
  requested_by, status, decided_by, decided_at}` persisted in
  `<state>/approvals/` so `tobyd` restarts keep them.
- Decided by: overlay (default, milestone 9), `toby approvals [<id>]`,
  web UI. First decision wins; events notify all clients.
- Before milestone 9: the attached CLI prints a one-line notice above the
  session output on a separate line after clearing (best effort) and the
  user answers with `toby approvals`.

---

## 17. Security model

- The guest is untrusted. Everything inside one VM is a single trust
  domain (the user may be root there).
- Host secrets never enter a guest, except the environment of an isolated
  MCP process in a services machine that the user configured with that
  secret.
- `tobyd`'s API socket and the runtime directory are 0700/0600 and never
  exposed to guests. Guests reach the host only through `toby-machine`
  (vsock) and virtio-fs (`toby-fs`).
- `toby-machine` and `toby-fs` are the attack surface from the guest:
  minimal parsing, strict size limits, fuzzed parsers, no policy decisions
  from guest input, only pre-registered listener IDs honored.
- `toby-fs`: beneath-only resolution, squashed identities, no setuid bits,
  no device nodes, read-only runtime tree.
- Cloud Hypervisor runs with its seccomp filters enabled (default).
- passt: no inbound forwarding; all inbound access is explicit forwards.
- Relay rejects non-host vsock peers.
- Web UI (§18): localhost only, one-time token → `SameSite=Strict` cookie,
  `Host`/`Origin` checks on every request and WebSocket upgrade.
- API authentication: Unix socket peer UID must equal the daemon's UID.

---

## 18. `tobyd` API (HTTP+JSON, WebSockets)

Served on `<rt>/tobyd.sock` for the CLI and, when enabled, on
`127.0.0.1:<port>` for the web UI. OpenAPI generated with `utoipa`.

```text
GET    /v1/daemon                          version, backend, linger, paths
GET    /v1/events                  (WS)    approvals, machine/session/attach/forward/build/MCP events

GET    /v1/images                          list
POST   /v1/images/prepare                  {default, mcp[], project, all, rebuild, pull} → build ids (§15.6)
POST   /v1/builds                          start build {source, arch, size} → build id
GET    /v1/builds/{id}                     status
GET    /v1/builds/{id}/logs        (WS)    stream
DELETE /v1/images/{id}                     refuse if referenced
POST   /v1/images/prune

GET    /v1/roots                           list (+ superseded flag)
POST   /v1/roots                           create {name, image}
POST   /v1/roots/{name}/reset
POST   /v1/roots/{name}/rebase             {image}
DELETE /v1/roots/{name}

GET    /v1/homes                           list
POST   /v1/homes                           create {name, username, uid, size}
DELETE /v1/homes/{name}

GET    /v1/machines                        list
POST   /v1/machines/ensure                 {home, root, ephemeral, resources} → machine (started)
POST   /v1/machines/{id}/stop
GET    /v1/machines/{id}/logs      (WS)
POST   /v1/machines/{id}/attachments       {host, at, read_only, pinned}
DELETE /v1/machines/{id}/attachments/{aid}
POST   /v1/machines/{id}/forwards          {direction, host, guest, pinned}
DELETE /v1/machines/{id}/forwards/{fid}

POST   /v1/sessions                        {machine|home+root, tool|argv, identity, tty, cwd, attach} → {id, session_socket}
GET    /v1/sessions                        list (all machines)
POST   /v1/sessions/{id}/kill
GET    /v1/sessions/{id}/io        (WS)    web terminal (proxied through tobyd; not used by the CLI)

GET    /v1/mcp                             configured servers and status
GET    /v1/mcp/{name}/logs         (WS)
GET    /v1/approvals                       list
POST   /v1/approvals/{id}                  {decision: approve|deny}
POST   /v1/web/token                       one-time URL for the web UI
```

Long operations (machine start, tool install) return quickly with an
operation ID and report progress through `/v1/events`; the CLI renders
progress from events.

---

## 19. Platform abstraction (macOS later, aarch64 soon)

Keep these boundaries platform-neutral from the first commit:

| Interface | Linux implementation | Later macOS implementation |
| --- | --- | --- |
| `Engine` | cloud-hypervisor child process | Virtualization.framework inside `toby-machine` (signed, virtualization entitlement) |
| `DiskStore` | qcow2 + backing files via `qemu-img` | raw disks, APFS clones |
| `FileShare` | `toby-fs` vhost-user virtio-fs | VZ virtio-fs shares (**VERIFY** runtime share changes) |
| `StreamTransport` | hybrid vsock Unix sockets | VZ virtio socket device |
| `Network` | passt vhost-user | VZ NAT |
| `ServiceManager` | systemd-user, direct | launchd, direct |
| `Paths` | XDG-style under `$HOME` | `~/Library/Application Support/Toby` etc. |

Rules: architecture is part of image, boot kit and builder identity from day
one; no Linux-specific types in `tobyd` core logic or `toby-api`; the guest
side is always Linux and identical on every host.

aarch64: the `toby` binary built for `aarch64-unknown-linux-musl`;
bootstrap uses Debian arm64 genericcloud; boot adaptation installs the
distro's arm64 kernel; bundled firmware for Cloud Hypervisor on aarch64 is
edk2 `CLOUDHV_EFI.fd` rather than rust-hypervisor-firmware (**VERIFY**).

---

## 20. CLI reference

```text
toby <tool> [--home H] [--root R] [--project PATH]… [--ephemeral] [--attach|--new] [--yolo] [--install] [--upgrade] [-- args…]
toby run -f <launch.toml>        # named launch file (§14.6)
toby exec [--root] [--home H --root R] [--cwd DIR] -- CMD…
toby shell [--root] [--home H --root R]

toby sessions ls | kill <id>
toby attach [<session>]           # reattach a session (tmux-style)

toby machine ls | stop <id>|--all | logs <id> [-f]
toby mount <host-path> [--at GUEST] [--ro] [--home H --root R] [--persist]
toby unmount <host-path|id>
toby forward add <host>[:<guest>] [--to-host] [--home H --root R] [--persist]
toby forward rm <id> | ls

toby image prepare [--all] [--default] [--mcp [NAME…]] [--project [PATH]] [--rebuild] [--pull]   # §15.6
toby image build [--dockerfile F] [--context DIR] | pull <ref> | import <oci-archive> | ls | rm <id> | prune
toby root ls | create <name> --image ID | reset <name> | rebase <name> [--image ID] | rm <name>
toby home ls | create <name> | rm <name>
toby builder bootstrap [--base <file>] [--clean] | status

toby mcp ls | logs <name> [-f] | restart <name>
toby approvals [<id> approve|deny]

toby daemon status | start | stop | restart | logs [-f]
toby linger on | off
toby config get|set <key> [value]
toby doctor                    # KVM, bundled cloud-hypervisor + firmware, passt, back end, linger, paths, substitutions
toby web                       # open the web UI with a one-time token
```

Verbs (decided): `toby attach` reattaches sessions; `toby mount` /
`toby unmount` attach and detach host paths.

`--persist` stores attachments/forwards in the (home, root) machine record
so they are recreated on every start.

`toby <tool>` flow:

1. Resolve home/root/image from flags, project config, global config.
2. Bootstrap (once, §15.2) and build the image if the root doesn't exist
   yet and the source has no image (with progress).
3. `POST /v1/machines/ensure` (starts if needed; linger warning if
   applicable).
4. Attach the project (session-scoped reference).
5. Tool reconcile (§16.1) with progress.
6. `POST /v1/sessions` and attach the terminal.
7. On session end: release the attachment reference; print exit status.

---

## 21. Web UI (milestone 10)

- Served by `tobyd` (`axum`) from an embedded bundle (`rust-embed`);
  server-rendered pages with htmx unless complexity demands a SPA.
- Pages: machines (start/stop, mounts, forwards), homes, roots
  (reset/rebase), images and builds (logs), sessions (list, kill), MCP
  status and logs, approvals queue.
- Browser terminals (xterm.js over `/v1/sessions/{id}/io`) are **not** part
  of M10 (decided); the API route stays reserved and is added after M11.
- Access: `toby web` → `POST /v1/web/token` → opens
  `http://127.0.0.1:<port>/login?token=…` → cookie. Localhost only.

---

## 22. Dependencies (initial selection)

| Area | Crates |
| --- | --- |
| async, HTTP | `tokio`, `hyper`, `hyper-util`, `axum`, `tower`, `tokio-tungstenite` (via axum ws) |
| TLS, downloads | `rustls`, `reqwest` (rustls) |
| serialization | `serde`, `serde_json`, `toml`, `ciborium` |
| CLI, terminal | `clap`, `crossterm`, later `alacritty_terminal` or `wezterm-term`, `ratatui` if useful |
| Linux | `rustix`, `nix` (pty, setuid), `zbus`, `inotify`, `rtnetlink` (helper) |
| vsock (guest) | `tokio-vsock` |
| virtio-fs | `vhost-user-backend`, `vhost`, `virtio-queue`, `vm-memory`, `fuse-backend-rs` |
| templating, patching | `minijinja`, `json-patch` / merge helpers, `toml_edit` |
| misc | `tracing`, `tracing-subscriber`, `ulid`, `sha2`, `utoipa`, `rust-embed`, `thiserror`, `anyhow` (binaries only) |
| MCP | `rmcp` (Toby MCP server) |

Disk images: `imago` (qcow2 creation).

External programs: bundled `cloud-hypervisor`, rust-hypervisor-firmware
and mkosi (§4); system-installed `passt` and `journalctl` (systemd-user
back end). No programs are downloaded at runtime. Inside builder machines
(from the default image): `podman`/`buildah`, `dracut`, `e2fsprogs`,
`python3`, `bubblewrap` and mkosi's other dependencies.

License policy in `deny.toml`: allow Apache-2.0, MIT, BSD-2/3, ISC, Zlib,
Unicode; review anything else.

---

## 23. Milestones

Each milestone ends with: fmt, clippy, tests, deny passing; docs for
user-visible behavior; acceptance criteria demonstrated.

### M0: Spikes (throwaway code, answers every VERIFY)

1. Builder boot: Cloud Hypervisor with rust-hypervisor-firmware boots
   Debian 13 genericcloud from an `imago`-created overlay; OEM-string
   credentials inject a unit that starts.
2. Boot adaptation by hand on two trees (a Debian tree from mkosi
   `Format=directory` and the current Arch `.toby/Dockerfile` image): distro
   kernel + dracut initramfs with the `99toby` module boot the exported
   ext4 root directly; the hook's `/run/systemd/system` units start.
   mkosi runs as root inside a VM with its tools tree and caches on a
   separate disk.
3. `passt --vhost-user` works as Cloud Hypervisor's network back end
   (or decide on the pasta-namespace fallback).
4. fuse-backend-rs `Vfs` + passthrough over vhost-user, unprivileged, with a
   passthrough mount added and removed while the guest runs; squashed
   identity; basic `git status` benchmark vs virtiofsd.
5. Hybrid vsock both directions; guest relay rejects non-host CIDs.
6. PTY over vsock: interactive latency and full-screen TUI correctness.
7. logind: confirm machines stop at logout without linger (systemd-user)
   and survive with linger; direct mode behavior under tmux with
   `KillUserProcesses=no`.

Exit: a written go/no-go per item; plan updated for any fallback.

### M1: Repository reset

Delete the Go code and old docs/packaging, create the workspace skeleton
(§5), the single `toby` binary with the subcommand tree from §3.3 (stubs)
and `argv[0]` dispatch, static musl build, CI, `AGENTS.md` for Rust,
`deny.toml`.
Acceptance: the static binary builds and runs every stub subcommand; CI
green.

### M2: Stable tier and first machine

`toby-proto` (headers, relay control, session protocol, machine control),
`toby-relay`, `toby-session`, `toby-machine` (vsock endpoints, control
socket, splicing), `toby-engine` (Cloud Hypervisor), a hand-made test
image (disk, kernel, initramfs from the M0 spike). CLI: `toby exec` /
`toby shell` against a manually started machine.
Acceptance: interactive shell works (colors, resize, Ctrl-C, full-screen
editor); detach and reattach with replay; relay restart does not kill the
session; `toby-machine` restart does not stop the VM.

### M3: Store, builder, images, roots, homes

`toby-store`, bootstrap, bundled mkosi + default mkosi configuration,
mkosi/Dockerfile/registry/archive builds, `toby image prepare`, boot
adaptation for the apt, dnf, pacman and zypper
families, home formatting jobs, roots and homes with reset/rebase, boot
helpers (net-up, user-setup, home-mount, links), custom image requirements
documented.
Acceptance: bootstrap from the pinned cloud image produces the default
image; the current Arch `.toby/Dockerfile` builds and boots unchanged;
machine boots with the home mounted as the user; network egress works; root
reset returns to image state; home persists across roots.

### M4: File sharing

`toby-vfs`, `toby-fs`, attach/detach with identity mapping and policies.
Acceptance: attach a project while a session runs; files created in the
guest as root or user appear owned by the host user; no escape via
symlinks (tests); detach refused while busy; benchmark recorded.

### M5: Control plane and supervision

`tobyd` API, desired state and reconciler, machine registry and pairing
rules, locks, idle stop, `toby-svc` (systemd-user + direct), unit files,
linger warning, `toby daemon`, `toby doctor`, sessions API, CLI rewritten
on the API.
Acceptance: `tobyd` restart during an active session is invisible;
machines survive `tobyd` restart in both back ends; idle stop works;
pairing conflicts reported clearly.

### M6: Forwards, models proxy, tools

Forwards (both directions, pinned/persisted), capabilities, `toby-proxy`
(models), tool manifests for Claude Code, Codex, OpenCode, generated
config patching, OAuth login forward.
Acceptance: `toby claude` end to end on a fresh home including login;
second tool joins the same machine; tool upgrade side by side; dev server
reachable via forward.

### M7: MCP, Toby MCP, approvals

HTTP MCP proxy, stdio machine placement, isolated placement with services
machines, `toby-connect`, Toby MCP git host actions, approvals (CLI + API).
Acceptance: an isolated MCP with a secret works and the tool machine never
sees the secret; `git_push` from a tool requires approval and uses host
credentials.

### M8: Live upgrade hardening

Protocol version negotiation tests (current and previous), side-by-side
stable binaries, helper versioning, upgrade scenarios scripted in
integration tests.
Acceptance: upgrade every control-tier component with sessions running and
no visible interruption beyond documented reconnects.

### M9: Terminal compositor

Terminal emulation, status bar, floating windows, overlay approvals as the
default, detach key handling.
Acceptance: Claude Code, Codex and OpenCode render correctly under the
compositor (including mouse, paste, alternate screen); approvals appear as
overlays without corrupting output.

### M10: Web UI

As §21.

### M11: Packaging and polish

Arch and Debian packages (the `toby` binary, units, dracut module, bundled
cloud-hypervisor and rust-hypervisor-firmware from `packaging/bundled.toml`
with their licenses, `passt` as a dependency), user docs, `image prune`, `root rebase`
indicators, error message review, aarch64 enablement.

---

## 24. Testing strategy

- Unit tests per crate; property tests for config merging and templating.
- Fuzzing: stream headers, relay control, session frames, `toby-fs`
  request handling (via `fuse-backend-rs` request fuzzing harness).
- Integration (KVM, `TOBY_TEST_KVM=1`): a small reference image built once
  and cached; tests for boot, exec, attach, forwards, reset, upgrades.
- Terminal tests: scripted PTY sessions with expected screen snapshots
  (using the same emulator library as the compositor).
- CI: GitHub-hosted Linux runners with `/dev/kvm` (**VERIFY** availability
  on the chosen runner class); otherwise a self-hosted runner.

---

## 25. Open questions (decide during implementation)

Decided (kept for reference):

1. Verbs: `toby attach` for sessions, `toby mount` / `toby unmount` for
   host paths (§20).
2. Cloud Hypervisor and rust-hypervisor-firmware are bundled in
   `/usr/lib/toby/`; passt is a system dependency; no program is downloaded
   at runtime.
3. No prebuilt images. Users' existing image sources are built and
   boot-adapted automatically; the default image is a bundled Dockerfile
   (§15).
4. Resource defaults as in §14.5.
5. Passwordless sudo on by default (`home.sudo = false` disables).
6. Browser terminals come after M11 (§21).
7. One services machine per isolated MCP server (§16.3).
8. Bootstrap uses the latest Debian 13 `genericcloud` build (§15.2),
   pinned to the distro release only, downloaded automatically once and
   checked against Debian's published `SHA512SUMS`, then fully upgraded;
   `--base <file>` is the offline override. Bundled artifacts are pinned by
   upstream release tag, never by stored hashes.

9. Image sources: mkosi configurations and container sources (Dockerfile,
   registry, OCI archive) are both supported for user and MCP images; the
   default image uses a bundled mkosi configuration built with a bundled
   mkosi; mkosi output is `Format=directory` for now (§15).

No open questions remain; new ones go here as they come up.
