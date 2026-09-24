# M0 spike results

Results of the milestone 0 spikes from `plans/vm-rewrite.md` (§23 M0). Each
item records what was run, the measurements, and a go/no-go. The plan has been
updated wherever a result changed a decision; the "Plan changes" list at the
end names each change.

Test host: x86_64, Linux 7.2, 22 CPUs, systemd 261, passt 2026_07_28.
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
--dns-forward 10.0.2.3 -D 10.0.2.3 --no-map-gw -t none -u none` with Cloud
Hypervisor `--net vhost_user=true,socket=<rt>/net.sock,vhost_mode=client`:
ICMP, TCP (HTTPS) and UDP egress work.

- With `--no-map-gw` (required so the guest cannot reach host loopback
  services), passt sends forwarded DNS to `0.0.0.0:53` unless the upstream is
  given explicitly. `--dns-host <first nameserver of the host resolv.conf>`
  fixes it (including a `127.0.0.53` stub resolver). Toby passes it always.
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

## 4. virtio-fs with fuse-backend-rs — GO (with a thread pool in M4)

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
  the kernel; Cloud Hypervisor sends no `RESET_DEVICE`. The kernel's later
  `INIT` must therefore reset the session instead of failing (`Vfs` rejects a
  second `INIT` by itself). The wrapper does this.
- Guest `umount` of a busy bind mount fails with "target is busy"; after a
  host-side removal the guest sees `ENOENT`, not a hang. The `Vfs` leaves an
  empty pseudo directory for a removed mount.

Benchmark (96k-file Linux checkout, guest with 4 vCPUs, `cache=auto`
equivalent):

| Operation | Toby spike (single-threaded) | virtiofsd 1.14 `--sandbox=none` |
| --- | --- | --- |
| `git status` cold (guest caches dropped) | 12.7 s | 11.8 s |
| `git status` warm | 0.77–0.84 s | 0.68–0.91 s |
| `grep -r` over `include/` cold | 0.41 s | 0.49 s |
| 1 GiB sequential write + fsync | 4.6 s | 1.6 s |

Metadata-heavy work matches virtiofsd. Bulk writes are slower because the
spike processes the queue on one thread; M4 adds a worker pool (as virtiofsd
does). The `virtiofsd` fallback is not needed.

## 5. Hybrid vsock — GO

- Host→guest: `CONNECT 1024\n` on `<rt>/vsock.sock` returns `OK <port>\n`; the
  guest listener sees peer CID 2.
- Guest→host: a guest connection to CID 2 port 1024 arrives on
  `<rt>/vsock.sock_1024`; data flows both ways.
- Guest-local connections to the relay port fail (CID 1 times out without
  `vsock_loopback`; CID 3 is `ENODEV`) and, where possible, arrive with a peer
  CID other than 2; the relay's "peer CID must be 2" check is sufficient.

## 6. PTY over vsock — GO

- Round-trip latency of 1-byte messages over hybrid vsock: 26 µs average
  (2000 samples). Echo throughput: about 410 MB/s.
- An interactive shell over vsock (sshd socket-activated on `AF_VSOCK`) was
  used throughout the spikes without perceptible latency. Full-screen TUI
  correctness depends only on byte transparency, which the splice path
  preserves; M2 acceptance tests it with the real session protocol.

## 7. logind and lingering — GO (by inspection; not exercised by logging out)

- The test host has `KillUserProcesses=no` and `Linger=no`; the user manager
  runs as `user@<uid>.service` in a `manager` session.
- Per logind semantics, units of the user manager (the `systemd-user` back end)
  stop when the last session of a non-lingering user ends and keep running with
  linger enabled; processes started outside the user manager (the `direct`
  back end, e.g. under tmux) survive logout only with `KillUserProcesses=no`.
- Ending every login session was not possible from the test session, so the
  behavior was not observed directly. The linger warning (§12.2) and the
  `toby daemon status` note for the direct back end cover both cases.

## Other findings

- GitHub-hosted runner KVM availability (§24) was not tested here; it is
  checked when CI is created in M1.
- The latest Cloud Hypervisor release is v53.0; it is the pinned version.
- mkosi's latest release is v27; it is the pinned version.

## Plan changes

1. Firmware: Cloud Hypervisor edk2 (`CLOUDHV.fd` / `CLOUDHV_EFI.fd`) replaces
   rust-hypervisor-firmware everywhere (§2, §4, §7.2, §9.3, §15.2, §19, M11).
2. Pinned versions: Cloud Hypervisor v53.0, edk2 `ch-97eeb7b09`, mkosi v27
   (§4).
3. passt invocation fixed, with `--no-map-gw` and an explicit `--dns-host`;
   passt lifetime and naming notes (§11.1).
4. `net-up` selects the interface by driver (§9.6).
5. mkosi invocation: run `bin/mkosi` directly; `--workspace-directory` on the
   cache disk; `python3-pefile` in the builder dependencies (§15.2, §15.3).
6. Adaptation runs in a private mount namespace, runs `systemd-hwdb update`;
   export uses `cp --one-file-system` (§15.3, §15.4).
7. `toby-fs`: repeated `INIT` resets the session; `.`/`..` lookups rejected;
   worker thread pool; dependency versions pinned (§10).
8. Leftover `qemu-img` references replaced by `imago` (§5, §19).
