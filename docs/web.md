# Web UI

```sh
toby web
```

prints a login link and opens it in your browser. The link works once,
within a minute; the browser then stays logged in until the daemon
restarts. The pages show machines (start, stop, mounts, forwards, logs),
sessions, approvals, images and builds, homes and roots, and MCP servers
with their logs, and update as things change.

The web UI listens on 127.0.0.1 only, on a free port, or on the port
`daemon.web_port` names:

```toml
[daemon]
web_port = 7474
```
