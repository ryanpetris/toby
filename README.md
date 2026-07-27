<p align="center">
  <img src="docs/logo.png" alt="Toby" width="280">
</p>

Toby runs development tools in private-home Linux sandboxes. Each launch uses
a verified OCI root filesystem and Bubblewrap without using Docker as its
runtime.

OpenCode, Claude Code, Codex, Copilot, Deep Agents Code, Grok, package
managers, and VCS clients see the selected projects and Toby-managed storage.
Host files such as `~/.ssh`, `~/.gnupg`, and ordinary tool configuration remain
outside the sandbox.

## Install

Toby supports Linux kernel 6.9 or newer, requires Bubblewrap (`bwrap`), and
must run as a non-root host user. See the
[detailed requirements](docs/installation.md#requirements) for host and
source-build prerequisites.

Download the current Debian package, Arch Linux package, or binary-only
`x86_64` or `arm64` archive from
[GitHub Releases](https://github.com/ryanpetris/toby/releases). For an archive
on `x86_64`:

```sh
curl -fLO \
  https://github.com/ryanpetris/toby/releases/latest/download/toby-linux-x86_64.tar.gz
tar -xzf toby-linux-x86_64.tar.gz
install -Dm755 toby tobyd tobys ~/.local/bin/
```

Ensure `~/.local/bin` is on `PATH`. Replace `x86_64` with `arm64` on an ARM64
host. To install the current source instead, clone the repository and run
`make go/install`.

Packages also install optional systemd user and system-wide socket units. See
[Installation and services](docs/installation.md) for package commands and
service setup.

## Get started

Create a project in the default location and launch a tool:

```sh
mkdir -p ~/Projects/my-app
cd ~/Projects/my-app
toby opencode my-app
```

The final argument is an environment name. It selects a persistent private home
independently of the application and project, so another tool can use the same
home:

```sh
toby codex my-app
```

Run an arbitrary command with `exec`:

```sh
toby exec my-app -- npm test
```

Use `--project` when the environment name and project directory differ:

```sh
toby claude review --project ~/Projects/my-app
```

Application sandboxes default to
`mcr.microsoft.com/devcontainers/javascript-node:24-bookworm`. Select another
prebuilt OCI image in `~/.config/toby/config.yaml`:

```yaml
sandbox:
  image: docker.io/library/node:24-bookworm-slim
  pull: if-missing
```

Toby also supports project launch files at `.toby/config.yaml`, reusable tool
profiles, MCP servers, models APIs, image and volume management, host Git
approval, and config-owned multi-project launches.

## Tools

The primary launch commands are grouped by purpose:

- AI coding: `opencode`, `claude`, `codex`, `copilot`, `dcode`, `grok`, and
  `t3`
- Development and helper tools: `exec`, `npm`, `uv`, `gh`, `glab`, `fj`,
  `emdash`, `speckit`, and `docker`

The `docker` tool is an explicit high-trust exception that exposes the host
Docker daemon through a run-scoped relay. Docker is not otherwise required or
used by Toby.

Run `toby --help` for the complete command list and `toby <command> --help` for
command-specific options.

## Documentation

The [documentation index](docs/README.md) links the getting-started guides,
configuration and tool references, runtime design, wire protocols, operations,
and contributor references.

## Development

Build and validate from the repository root:

```sh
make build
make test
make vet
make check/fmt
```

See [AGENTS.md](AGENTS.md) for repository conventions and the complete
development workflow.

## License

Toby is available under the [MIT License](LICENSE).
