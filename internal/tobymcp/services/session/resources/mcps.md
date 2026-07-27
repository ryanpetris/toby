# Toby-managed MCP resources

Toby's per-user agent owns native MCP acquisition, reuse, and teardown. A run
receives only its immutable target allowlist and a sandbox-visible connector
capability; upstream URLs, headers, commands, environment values, credentials,
and host paths remain outside the sandbox.

Local stdio targets start one fresh Bubblewrap process per connector. Local HTTP
targets may share a matching background process while each connector keeps its
own MCP protocol session. Remote HTTP targets keep their endpoint and headers
inside the agent. Resources are acquired before publication and are released
automatically after their leases and connectors end.

Use `toby://session/mcps` to inspect the bounded status captured for this run.
