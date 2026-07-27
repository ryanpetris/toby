# Toby Documentation

Toby runs development tools in private-home Bubblewrap sandboxes backed by
verified OCI root filesystems. This index separates user workflows from runtime
and contributor reference material.

## Start here

- [README](../README.md) - install Toby, launch the first tool, and see the
  shortest command and configuration examples.
- [Installation and services](installation.md) - release artifacts,
  release contents, package installation, and optional systemd user or
  system-wide agent activation.
- [Examples](examples.md) - complete examples for images, projects, profiles,
  MCP servers, models APIs, permissions, T3 Code, and the Docker tool.
- [Configuration](configuration.md) - the strict host and launch schemas,
  precedence, path resolution, substitutions, defaults, and warning controls.
- [Tools](tools.md) - built-in tool catalog, installation behavior, tool
  volumes, generated native files, and launch integration.

## Runtime and integrations

- [Sandbox and integration details](sandbox.md) - sandbox-visible paths,
  private homes, projects, mounts, terminal modes, MCP, models, and Docker's
  explicit exception.
- [Architecture](architecture.md) - process ownership, launch sequence, OCI and
  volume storage, Bubblewrap execution, agent resources, and trust boundaries.
- [Protocols](protocols.md) - exact agent and sandbox gRPC contracts, stream
  ordering, IDs and leases, resource logs, models HTTP routing, host actions,
  and private helper protocols.
- [Glossary](glossary.md) - canonical terminology used by code, configuration,
  CLI output, and documentation.

## Operations

- [Storage and agent management](management.md) - image build, import, catalog,
  and removal commands; volume management; agent lifecycle; logs; and caches.
- [Debugging sandbox startup](debugging-sandbox-startup.md) - startup
  presentation, structured diagnostics, host prerequisites, OCI and storage
  checks, agent inspection, resource troubleshooting, and terminal behavior.

## Contributor reference

- [Agent guide](../AGENTS.md) - repository structure, design rules, generated
  code, testing, documentation synchronization, and commit gates.
- [Dependency licenses](dependencies.md) - canonical direct and indirect Go
  module inventory and accepted licenses.
- [`proto/toby/agent/v1/agent.proto`](../proto/toby/agent/v1/agent.proto) -
  authored agent schema.
- [`proto/toby/sandbox/v1/sandbox.proto`](../proto/toby/sandbox/v1/sandbox.proto) -
  authored run-scoped sandbox resource schema.
