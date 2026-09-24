# Web UI

```sh
toby web
```

prints a login link and opens it in your browser. The link works once,
within a minute; that browser then stays logged in until the daemon
restarts. The pages show machines (start, stop, mounts, forwards, logs),
sessions, approvals, images and builds, homes and roots, and MCP servers
with their logs, and update as things change.

The web UI listens on 127.0.0.1 only, on a free port, or on the port
`daemon.web_port` names:

```toml
[daemon]
web_port = 7474
```

From another computer, forward the port over SSH (`ssh -L
7474:127.0.0.1:7474 host`) and open the link with your local port.

The login link is on the browser's command line while the browser starts,
where other users of the computer can read it; on a shared computer, open
the printed link yourself in a browser that is already running.

The pages use tobyd's API, which `GET /v1/openapi.json` describes, on the
daemon's socket or, logged in, on the web UI's port.
