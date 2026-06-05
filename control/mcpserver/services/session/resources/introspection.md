# Toby Introspection

Toby exposes session resources with current sandbox and proxy state. These resources are read-only text resources intended for diagnosis and agent orientation.

Session resources:

- `toby://session/runtime`: current Toby version, debug mode, sandbox runtime, sandbox-visible paths, selected manager environment, and runtime-defined details.
- `toby://session/mcps`: configured MCP server status and redacted runtime details.
- `toby://session/tools`: primary tool, active tools, available Toby tools, tool groups, and provider summaries.
- `toby://session/projects`: visible projects, additional binds, and managed mount summaries.

If your MCP client cannot read resources directly, the `resources.read` tool returns the same content: pass `uris` with one or more of the URIs above, or omit `uris` to read every available resource.

Normal mode returns sandbox-visible paths and safe summaries. Debug mode may include host paths, Docker volume names, setup paths, container names, and local MCP host ports when those details help diagnose a session.

Toby introspection never exposes configured provider or MCP URLs, headers, commands, argv, or environment values, even in debug mode.
