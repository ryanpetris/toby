# Toby Sandbox

You are running inside a Toby private-home sandbox. The host's Git
configuration, SSH agents, credential helpers, and signing keys are not exposed
inside the sandbox.

Launch-selected MCP servers, including the built-in Toby MCP, arrive through
this client's MCP interface. Clients present those servers and tools
differently: prefixes, server folders, slash commands, or other groupings. Use
the names and grouping your client shows. Do not assume one spelling.

When a task needs host Git credentials, identity, or signing, use the Toby MCP
Git tools instead of sandbox-local `git` for that step. Those tools commit
already-staged files, fetch remotes, push the current branch and requested
tags, rebase, and create annotated tags. They do not stage files.

Repository names are relative to `XDG_PROJECTS_DIR`. Nested repositories are
supported when they are visible to the current launch. Stage files and inspect
working-tree changes with the sandbox's ordinary Git CLI before calling a host
Git tool.

MCP and models capabilities are selected for each launch and reached through
Toby's sandbox-local connectors. The host agent socket and credentials are not
mounted into the sandbox.

The Toby MCP also exposes read-only introspection resources. If your client can
read MCP resources, start with:

- `toby://docs/git`, `toby://docs/mcps`, and
  `toby://docs/introspection` for capability guides
- `toby://session/runtime` for the current Toby and sandbox runtime
- `toby://session/mcps` for configured MCP capabilities
- `toby://session/tools` for active and available tools
- `toby://session/projects` for visible projects and mounts

If it cannot, use the Toby MCP resource-reader tool with those URIs, or with no
arguments to read them all.

Introspection does not expose models or MCP URLs, headers, commands,
arguments, or environment values.
