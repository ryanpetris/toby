# Toby session introspection

Toby exposes four data-only resources for the current native run:

- `toby://session/runtime`: Toby version, debug mode, native runtime, and
  sandbox-visible runtime paths.
- `toby://session/mcps`: configured MCP target status and safe runtime policy.
- `toby://session/tools`: active and available tools plus models endpoint summaries.
- `toby://session/projects`: visible project paths, binds, and managed mounts.

The launch captures one strictly validated snapshot before the agent publishes
the run. Introspection is rendered only from that snapshot and never consults
live configuration, sandbox state, process state, or host paths.

The snapshot schema cannot carry models or MCP URLs, headers, commands,
arguments, environment values, credentials, capability paths, or host source
paths. Debug mode does not weaken those exclusions.
