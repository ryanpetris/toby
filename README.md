<p align="center">
  <img src="docs/logo.png" alt="Toby" width="280">
</p>

Toby is a tool for running development tools such as Claude Code, Codex and
OpenCode inside KVM virtual machines. Each machine is to boot its image's own
kernel and systemd, run tools as an ordinary user with a private home disk, and
see only the project directories you attach, while credentials and approval
decisions stay on the host.

Toby is under development and cannot run tools yet; most commands report that
they are not implemented. The design and milestones are in
[`plans/vm-rewrite.md`](plans/vm-rewrite.md).

## Requirements

- Linux on x86_64 with `/dev/kvm` accessible to your user.
- passt with vhost-user support.

## Building

Toby is a Rust workspace that builds one binary, `toby`. The toolchain version
is pinned in `rust-toolchain.toml`.

```sh
make check    # format check, clippy, tests, cargo-deny
make static   # fully static release binary (needs x86_64-linux-musl-gcc)
```

The static binary is written to
`target/x86_64-unknown-linux-musl/release/toby`. The musl C compiler comes from
the `musl` package on Arch Linux and `musl-tools` on Debian and Ubuntu.

## License

MIT. See [`LICENSE`](LICENSE).
