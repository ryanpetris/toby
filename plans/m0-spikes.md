# M0 spike results

Results of the milestone 0 spikes from `plans/vm-rewrite.md` (§23 M0). Each
item records what was run, the measurements, and a go/no-go. The plan states
the resulting decisions.

Test host: x86_64, Linux 7.2, systemd 261, passt 2026_07_28.
Versions tested: Cloud Hypervisor v53.0 (static release), rust-hypervisor-firmware
0.5.0, Cloud Hypervisor edk2 `CLOUDHV.fd` release `ch-97eeb7b09`, mkosi v27,
Debian 13 `genericcloud` amd64 (`latest`, kernel 6.12.107, systemd 257.13),
fuse-backend-rs 0.14.0, vhost-user-backend 0.21.0, imago 0.2.5.

## 1. Builder boot — GO (with a firmware change)

- `imago` `Qcow2::create_builder(...).backing(<path>, "qcow2")` produced an
  overlay that `qemu-img check` reports clean, and Cloud Hypervisor opened it
  with `image_type=qcow2,backing_files=on`.
- rust-hypervisor-firmware 0.5.0 finds the Debian ESP and loads
  `\EFI\BOOT\BOOTX64.EFI` (shim), but shim aborts with
  `import_mok_state() failed: Unsupported`: the firmware lacks the UEFI variable
  services shim needs. **No-go for rust-hypervisor-firmware.**
- Cloud Hypervisor's edk2 build (`CLOUDHV.fd`) boots the same overlay through
  shim and GRUB to a running system in about 20 s (including cloud-init waiting
  for a datasource).
- `--platform oem_strings=[...]` with
  `io.systemd.credential.binary:systemd.extra-unit.<unit>=<base64>` and
  `io.systemd.credential.binary:systemd.unit-dropin.multi-user.target~toby=<base64>`
  reached systemd 257 as system credentials; the injected unit started
  (`Received regular credentials: systemd.extra-unit..., systemd.unit-dropin...`).
- `ssh.authorized_keys.root` delivered the same way plus systemd's
  `sshd-vsock.socket` gives a root shell over hybrid vsock port 22 (useful for
  development; not used by Toby).
- The Debian cloud image grows its root partition to the overlay's virtual
  size on first boot.
- `PUT vm.power-button` shuts the guest down cleanly (about 1 s). Once, a
  power-button request was not honored; the plan's escalation (shutdown API,
  then SIGKILL) is required.

Decision: bundle Cloud Hypervisor's edk2 firmware (`CLOUDHV.fd` on x86_64,
`CLOUDHV_EFI.fd` on aarch64, BSD-2-Clause-Patent) instead of
rust-hypervisor-firmware on every architecture.

## 2. Boot adaptation — GO

Both trees were built inside the bootstrap builder (the Debian cloud image
provisioned with `python3 python3-pefile bubblewrap buildah podman e2fsprogs
dracut dracut-core debian-archive-keyring apt dpkg zstd cpio
systemd-container uidmap git ca-certificates fuse-overlayfs`), adapted, exported
to a bare ext4 disk (serial `out`), and booted directly with
`--kernel/--initramfs` and
`root=/dev/disk/by-id/virtio-root rw console=hvc0 toby.version=<ver>`.

| Tree | Build | Adaptation | Export | Result |
| --- | --- | --- | --- | --- |
| Debian 13 via mkosi `Format=directory` (422 MiB) | 38 s cold, 1.7 s incremental | 5.5 s | 1.7 s | boots; `99toby` unit ran |
| current Arch `.toby/Dockerfile` via `buildah build --layers` (2.0 GiB) | 33 s | 13 s (pacman: kernel, dracut) | 6.4 s | boots; `99toby` unit ran |
| Fedora 43 via mkosi with `--tools-tree=default` (297 MiB) | 88 s cold | — | — | tree built with kernel 7.2.7 |

- The dracut pre-pivot hook wrote units into `/run/systemd/system` and a
  `multi-user.target.wants` symlink; `/run` carried into the real root, and the
  unit started after switch-root (`TOBY-99-OK <ver>`, root is `/dev/vda` ext4).
  `toby.version=` from the kernel command line was readable with `getarg`.
- Guest disk device links come from serials exactly as planned:
  `/dev/disk/by-id/virtio-{root,home,cache,out}`.
- Direct kernel boot reaches `multi-user.target` in a few seconds; the guest
  powered off 8.5 s after VM start including a power-button round trip.
- mkosi: `bin/mkosi` is a shell wrapper, so it is run directly, not as
  `python3 bin/mkosi`. It needs `python3-pefile` even with `Bootable=no`.
  Without `--workspace-directory` on the cache disk mkosi copies the finished
  tree across devices.
- Adaptation must run in a private mount namespace (`unshare -m`): a leaked
  recursive `/sys` bind inside the tree made the export `cp -a` block forever
  on a sysfs file. Export copies with `--one-file-system` as a second guard.
- Arch: installing `linux` triggers the image's own dracut hook (harmless);
  `systemd-hwdb update` is missing from container images and should be run by
  adaptation.
- Buildah inside the builder works as root with `--network host` and
  `/var/lib/containers` bind-mounted from the cache disk.

## 3. passt vhost-user networking — GO (with required flags)

`passt --vhost-user -s <rt>/net.sock -f -4 -a 10.0.2.15 -n 24 -g 10.0.2.2
--dns-forward 10.0.2.3 -D 10.0.2.3 --dns-host 127.0.0.53 --no-map-gw -t none
-u none` with Cloud Hypervisor
`--net vhost_user=true,socket=<rt>/net.sock,vhost_mode=client`: ICMP, TCP
(HTTPS), UDP and forwarded DNS work. `--no-map-gw` keeps host loopback
services unreachable from the guest.

- Without `--dns-host`, forwarded DNS went to `0.0.0.0:53`: `-D` makes passt
  skip importing the host's nameservers, so the forwarder has no upstream.
  (pasta with the same flags but without `-D` forwarded DNS correctly.) The
  upstream must be an IPv4 address because guest networking is IPv4 only
  (`-4`); the test host's first nameserver was the `127.0.0.53` stub.
- passt in vhost-user mode keeps running after the VMM disconnects; it must be
  stopped with its machine (unit `StopWhenUnneeded`/`BindsTo`, or the direct
  supervisor). On Arch the process re-executes as `passt.avx2`, so it must be
  tracked by PID, never by name. It leaves `<socket>.repair` behind, which is
  removed before start.
- The guest NIC name follows the PCI slot (`ens3`, `ens5`, ... depending on
  other devices), so `net-up` selects the virtio-net interface by driver, not
  by name.
- The Debian cloud image has no network without a cloud-init datasource;
  Toby's own `net-up` configures every machine, including the bootstrap
  builder.

## 4. virtio-fs with fuse-backend-rs — GO

A spike back end on `vhost-user-backend` 0.21 (`vhost` 0.15, `virtio-queue`
0.17, `vm-memory` =0.17.1, matching `fuse-backend-rs` 0.14 with feature
`vhost-user-fs`) served a `Vfs` with a read-only `/runtime` passthrough and
`/projects/<id>` passthroughs added and removed over a control socket, all as
the unprivileged host user.

- Passthrough works unprivileged with `inode_file_handles = false` (inodes are
  kept as `O_PATH` descriptors; no `CAP_DAC_READ_SEARCH` needed).
- Identity squash is a wrapper file system above the `Vfs`: `id_remap` sets the
  request context to uid/gid 0, which makes the passthrough skip its
  credential switching so every operation runs as the host user; replies map
  host-user ownership to the guest user and everything else to 65534. Tested
  from the guest as root and as uid 1000: files land owned by the host user;
  `chown` to the guest user or root succeeds as a no-op, other owners get
  `EPERM`; `mkfifo` and `mknod c` get `EPERM`; `chmod 4755` lands as `0755`.
- A read-only wrapper returns `EROFS` for every write even after guest root
  remounts the share read-write.
- Symlinks are returned to the guest and resolved there; a
  `../../../../etc/shadow` link inside an attachment read the guest's file.
- `Vfs::lookup` accepts `.` and `..` (for NFS export); the wrapper rejects both
  so no lookup can walk above an attachment root.
- edk2's `VirtioFsDxe` in the firmware sends its own `FUSE_INIT` (7.31) before
  the kernel; Cloud Hypervisor sends no `RESET_DEVICE`. `Vfs` rejects a second
  `INIT` by itself; the spike's wrapper called `destroy` and let the kernel's
  `INIT` through, but `Vfs::init` had already narrowed its stored options to
  the firmware's few flags, and `destroy` does not restore them. The kernel
  session then ran without `MAX_PAGES`, `WRITEBACK_CACHE` and similar
  options.
- Guest `umount` of a busy bind mount fails with "target is busy"; after a
  host-side removal the guest sees `ENOENT`, not a hang. The `Vfs` leaves an
  empty pseudo directory for a removed mount.

Benchmark (96k-file Linux checkout, guest with 4 vCPUs, `cache=auto`
equivalent). Both back ends process each queue on one thread (virtiofsd's
`--thread-pool-size` defaults to 0). The firmware-boot columns ran in the
Debian 13 cloud image (kernel 6.12); the direct-boot columns ran in the Arch
test image built in spike 2 (kernel 7.2). Compare columns within the same
guest.

| Operation | Toby spike, firmware boot | virtiofsd 1.14, firmware boot | Toby spike, direct kernel boot | virtiofsd 1.14, direct kernel boot |
| --- | --- | --- | --- | --- |
| `git status` cold (guest caches dropped) | 12.7 s | 11.8 s | 11.8 s | 12.2 s |
| `git status` warm | 0.77–0.84 s | 0.68–0.91 s | 0.72–0.81 s | 0.73–0.80 s |
| `grep -r` over `include/` cold | 0.41 s | 0.49 s | 0.51 s | 0.47 s |
| 1 GiB sequential write + fsync | 4.6 s | 1.6 s | 1.4 s | 4.0 s |

The firmware-boot write gap came from the narrowed options above: with the
kernel's full option set (direct kernel boot, one `INIT`), the spike matched
virtiofsd on metadata work and wrote faster. `toby-fs` therefore rebuilds the
`Vfs` with its original options when a new `INIT` arrives. The virtiofsd
fallback is not needed.

- With `inode_file_handles = false` each looked-up inode holds an `O_PATH`
  descriptor until the guest forgets it; the benchmark ran with a raised
  descriptor limit, which `toby-fs` sets itself.

## 5. Hybrid vsock — GO

- Host→guest: `CONNECT 1024\n` on `<rt>/vsock.sock` returns `OK <port>\n`; the
  guest listener sees peer CID 2.
- Guest→host: a guest connection to CID 2 port 1024 arrives on
  `<rt>/vsock.sock_1024`; data flows both ways.
- Guest-local connections to the relay port fail (CID 1 times out without
  `vsock_loopback`; CID 3 is `ENODEV`) and, where possible, arrive with a peer
  CID other than 2; the relay's "peer CID must be 2" check is sufficient.

## 6. PTY over vsock — GO for latency; terminal behavior tested in M2

- Round-trip latency of 1-byte messages over hybrid vsock: 26 µs average
  (2000 samples). Echo throughput: about 410 MB/s.
- An interactive shell over vsock (sshd socket-activated on `AF_VSOCK`) was
  used throughout the spikes without perceptible latency.
- Resize, signals and full-screen programs over the session protocol were not
  tested here; they are part of M2's acceptance criteria.

## 7. logind and lingering — not exercised; to be confirmed in M5

- The test host has `KillUserProcesses=no` and `Linger=no`; the user manager
  runs as `user@<uid>.service` in a `manager` session.
- Per logind semantics, units of the user manager (the `systemd-user` back end)
  stop when the last session of a non-lingering user ends and keep running with
  linger enabled; processes started outside the user manager (the `direct`
  back end, e.g. under tmux) survive logout only with `KillUserProcesses=no`.
- Ending every login session was not possible from the test session, so the
  behavior was not observed. M5's acceptance criteria include logout with and
  without linger for the `systemd-user` back end and logout with
  `KillUserProcesses=no` for the `direct` back end.

## Other findings

- GitHub-hosted runner KVM availability (§24) was not tested here; it is
  checked when CI is created in M1.
- The latest Cloud Hypervisor release is v53.0; it is the pinned version.
- mkosi's latest release is v27; it is the pinned version.
