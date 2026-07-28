# Installation and Services

## Requirements

Toby requires:

- Linux kernel 6.9 or newer;
- Bubblewrap (`bwrap`);
- Pasta (`pasta`) from the `passt` package for local MCP resources configured
  with `network: private`;
- `nsenter` from the `util-linux` package for those private MCP resources;
- a non-root host user;
- unprivileged user namespaces and Bubblewrap overlay support; and
- an absolute, accessible `XDG_RUNTIME_DIR` for sandbox and agent operations.

`toby`, `tobyd`, and direct host invocations of `tobys` exit before
initialization when their effective host UID is zero. Namespace-root lifecycle
commands remain unprivileged: the sandbox helper accepts namespace UID zero
only when `/proc/self/uid_map` maps it to a nonzero parent UID.

Building from source additionally requires the Go version declared in `go.mod`
and the Protocol Buffers compiler. The Makefile installs the pinned Go-based
generator and analysis tools when needed.

Dockerfile sandbox sources and `toby image build` additionally require
`buildah`. Registry images and OCI archive sources do not require Buildah.

## Release artifacts

Toby release artifacts support Linux on `x86_64` and `arm64`. Each release
contains:

- a binary-only `tar.gz` archive;
- a Debian package; and
- an Arch Linux package.

Packages install `toby`, `tobyd`, and `tobys` beneath `/usr/bin`, depend on
Bubblewrap, `passt`, and `util-linux`, include the MIT license, and install
optional systemd user and system-wide units. Package installation reloads the
system systemd manager but does not enable or start any unit.

## Binary archive

Download the archive matching the host architecture from
[GitHub Releases](https://github.com/ryanpetris/toby/releases). The AMD64
artifact uses `x86_64` in its filename; the ARM64 artifact uses `arm64`.

```sh
tar -xzf toby-linux-x86_64.tar.gz
install -Dm755 toby tobyd tobys ~/.local/bin/
```

Replace `x86_64` with `arm64` on an ARM64 host. The archive contains only the
three binaries, so it does not install systemd units.

## Debian package

Install a downloaded package with APT so its Bubblewrap dependency is resolved:

```sh
sudo apt install ./toby_VERSION_linux_x86_64.deb
```

Use the `arm64` package on an ARM64 Debian-family system.

## Arch Linux package

Install a downloaded package with Pacman:

```sh
sudo pacman -U ./toby_VERSION_linux_x86_64.pkg.tar.zst
```

Use the `arm64` artifact on an Arch Linux ARM system.

## Build from source

Install all three binaries to `GOBIN`, or to the default Go binary directory
when `GOBIN` is unset:

```sh
make go/install
```

To build into the repository instead:

```sh
make build
install -Dm755 bin/toby bin/tobyd bin/tobys ~/.local/bin/
```

The generated protobuf Go files are intentionally absent from the repository.
Source builds must run through the Makefile, which runs `make gen/grpc` before
the Go build. A direct remote `go install` cannot perform that generation and
is not a supported installation path.

`make release/check` validates the GoReleaser configuration.
`make release/snapshot` creates local archives and packages beneath `dist/`.

## systemd agent activation

The packages install two alternatives. Both run `tobyd --persistent`, so the
activated agent does not stop when it becomes idle. Both service units give
graceful shutdown 50 seconds, covering
client notification, agent-transport drainage, agent resource teardown, and
process finalization before systemd may force termination.

### User unit

Enable socket activation for the current user:

```sh
systemctl --user enable --now tobyd.socket
```

The socket is `$XDG_RUNTIME_DIR/toby/agent.sock`. It starts `tobyd.service`
on the first connection. To disable activation and stop the current agent:

```sh
systemctl --user disable --now tobyd.socket
systemctl --user stop tobyd.service
```

Enabling `tobyd.service` directly starts the persistent agent with the user
manager instead of waiting for the first connection.

### System-wide template

An administrator can enable one socket for a named user:

```sh
sudo systemctl enable --now tobyd@USERNAME.socket
```

The socket is:

```text
/run/toby/users/USERNAME/toby/agent.sock
```

It is mode `0600` and owned by `USERNAME`. The matching service runs as that
user. systemd owns creation and applies mode `0700` and the service user's
ownership to the two per-user Toby runtime directories when the service starts.
Toby clients use the normal `$XDG_RUNTIME_DIR` endpoint when it exists and fall
back to this system-wide endpoint only when the normal endpoint is absent.

Disable activation and stop the current agent with:

```sh
sudo systemctl disable --now tobyd@USERNAME.socket
sudo systemctl stop tobyd@USERNAME.service
```

If both the user and system-wide sockets exist, the normal user endpoint wins.
The other socket does not cause an error, but it can still retain or activate a
separate agent until its unit is stopped.

`toby agent stop` asks the current agent to shut down, but it does not stop
either systemd socket. A subsequent client connection can therefore activate a
new agent.
