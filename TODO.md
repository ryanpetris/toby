# TODO

## Give each machine its own copy of the binary instead of versioned installs

Guests run the host's `toby` binary, served by toby-fs from the versions
directory as `/run/toby/fs/versions/<version>/toby`. A machine keeps
using the version it booted with: its relay, its sessions and the guest
helpers name that versioned path. Because a package manager deletes the
files of the old package on upgrade, packages install the binary at
`/usr/lib/toby/dist/toby` and a post-install hook
(`toby internal install-version`) copies it into
`/usr/lib/toby/versions/<version>/`, which the package does not own, and
switches `current`. Unused versions are removed by a `/proc` scan (the
install hook, `toby-versions.timer`, and `collect-versions --uninstall`
before removal).

Alternative: each machine copies the binary into its own runtime
directory when it starts and serves that copy to the guest. The package
could then own a plain `/usr/lib/toby/toby`, and the versions directory,
the `current` link, version garbage collection, the install hooks and
the timer would go away.

To work out:

- The cost: one copy (about 20 MB) per running machine.
- How guests name the binary (relay, sessions, helpers, `/run/toby/bin`
  links) without a version directory.
- How tobyd detects host processes running an older binary so it can
  restart them after an upgrade (plan §3.2), which today compares
  version directories.
- What `SessionInfo.version` and the relay's reported version mean then.
- The builder machines, which also run the host binary.
- Plan §3.3, §4 and §9.4, docs/install.md and the packaging scripts.
