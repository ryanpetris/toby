# Toby Sandbox

You are running inside a Toby private-home sandbox. The host's Git
configuration, SSH agents, credential helpers, and signing keys are not exposed
inside the sandbox.

Use the Toby MCP Git tools for operations that require host credentials,
identity, or signing:

- `git.commit` commits files already staged inside the sandbox.
- `git.fetch` fetches remote refs.
- `git.push` pushes the current branch and, when requested, tags.
- `git.rebase` starts, continues, or aborts a rebase.
- `git.tag` creates an annotated tag.

Repository names are relative to `XDG_PROJECTS_DIR`. Nested repositories are
supported when they are visible to the current launch. Stage files and inspect
working-tree changes with the sandbox's ordinary Git CLI before calling a host
Git tool.

MCP and models capabilities are selected for each launch and reached through
Toby's sandbox-local connectors. The host agent socket and credentials are not
mounted into the sandbox.

The Toby MCP exposes read-only introspection resources:

- `toby://docs/git`, `toby://docs/mcps`, and
  `toby://docs/introspection` explain the available capabilities.
- `toby://session/runtime` describes the current Toby and sandbox runtime.
- `toby://session/mcps` describes configured MCP capabilities.
- `toby://session/tools` describes active and available tools.
- `toby://session/projects` describes visible projects and mounts.

Introspection does not expose models or MCP URLs, headers, commands,
arguments, or environment values.
