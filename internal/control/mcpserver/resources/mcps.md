# Toby-Managed MCPs

Toby-configured MCP servers are exposed to sandboxed tools through per-run proxy URLs while preserving their configured names.

Remote MCP entries are opened by the host Toby process. Headers and host-side substitutions stay on the host and are not exposed through introspection.

Local MCP entries are Toby-managed sidecars for the current session. Toby registers their proxy URLs before the main tool starts, then starts sidecars asynchronously. Stdio sidecars are bridged to streamable HTTP by the host Toby process. HTTP sidecars are proxied through their configured port and path.

Lifecycle tools:

- `mcp.start`: start a configured local Toby-managed MCP sidecar.
- `mcp.stop`: stop a configured local Toby-managed MCP sidecar.
- `mcp.restart`: restart a configured local Toby-managed MCP sidecar.

Use `toby://session/mcps` to inspect configured MCP status. That resource redacts MCP URLs, headers, commands, argv, and environment values. Lifecycle tools apply only to local Toby-managed sidecars; remote MCPs are proxied endpoints and are not started or stopped by these tools.

If an MCP should not be proxied by Toby, configure it directly in the launched tool's own MCP configuration instead of Toby config.
