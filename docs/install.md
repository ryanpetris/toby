# Installing Toby

## Requirements

- Linux on x86_64 or aarch64.
- KVM: `/dev/kvm` readable and writable by you (usually the `kvm` group).
- passt 2025_01_21 or newer.
- A systemd user instance, or the `direct` back end (see
  [Machines](machines.md)).

Cloud Hypervisor, its firmware and mkosi come with Toby's packages.

## Packages

Build a package from this repository and install it:

```sh
cd packaging/arch && makepkg -si                  # Arch Linux
packaging/debian/build-deb.sh out                 # Debian and Ubuntu
sudo apt install ./out/toby_*.deb
```

Building needs Rust with its musl target (`rustup target add
x86_64-unknown-linux-musl`), a musl C compiler (`musl` on Arch,
`musl-tools` on Debian and Ubuntu), `curl`, `jq` and `xz`. The build downloads
the pinned Cloud Hypervisor, firmware and mkosi releases named in
`packaging/bundled.toml` and checks the release files against the digests
their releases publish.

The package installs Toby as `/usr/lib/toby/versions/<version>/toby`.
Machines keep running the version they started with, so an upgrade leaves
older versions in place until nothing uses them; `toby-versions.timer`
removes them daily (enable it on Arch: `systemctl enable --now
toby-versions.timer`). After an upgrade, `toby daemon restart` moves your
daemon to the new version; machines move when they next start.

## First run

```sh
toby doctor        # checks KVM, the programs and the daemon
toby linger on     # keeps machines running after you log out
toby claude        # or another tool
```

The first launch builds the default image, which takes several minutes;
`toby image prepare` builds it, and every other image your configuration
needs, ahead of time.

## Without a package

Build the static binary (`make static`) and lay it out as a versions
directory, then point Toby at it and at the other programs:

```sh
mkdir -p ~/toby/versions/0.17.0
cp target/x86_64-unknown-linux-musl/release/toby ~/toby/versions/0.17.0/
ln -sfn 0.17.0 ~/toby/versions/current
```

```toml
# ~/.config/toby/config.toml
[daemon]
backend = "direct"

[programs]
versions = "~/toby/versions"
cloud_hypervisor = "/path/to/cloud-hypervisor"
firmware = "/path/to/CLOUDHV.fd"
share = "/path/to/share"          # mkosi/, images/default/ and dracut/99toby/
```

The share directory holds an unpacked mkosi release as `mkosi/`, and
`packaging/images/default` and `packaging/dracut/99toby` from this
repository as `images/default/` and `dracut/99toby/`. Switching `current`
while machines run should hold `flock` on `versions/.lock`.
