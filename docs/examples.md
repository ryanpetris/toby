# Examples

These examples assume:

- Linux with Bubblewrap and unprivileged user namespaces;
- a non-root host user;
- an absolute, accessible `XDG_RUNTIME_DIR`;
- projects beneath `${XDG_PROJECTS_DIR:-~/Projects}`.

## Choose a root image

Toby defaults application sandboxes to
`mcr.microsoft.com/devcontainers/javascript-node:24-bookworm` and pulls it
only when it is missing. A host configuration can select another image:

```yaml
# ~/.config/toby/config.yaml
sandbox:
  image: docker.io/library/node:24-bookworm-slim
  pull: if-missing
```

Use an immutable digest for reproducible launches:

```yaml
sandbox:
  image: ghcr.io/example/toby-root@sha256:...
  pull: never
```

## Launch a coding tool

```sh
mkdir -p ~/Projects/my-app
toby opencode my-app
```

The environment `my-app` and current profile select the private home. The
project defaults to `~/Projects/my-app`.

Use a different private-home name:

```sh
toby codex review-home --project my-app
```

Relative `--project` paths always resolve from `XDG_PROJECTS_DIR`, including
multi-segment paths such as `teams/platform/api`. With
`settings.allowExternalProjects: true`, a resolved relative path such as
`../outside` or an absolute path may select a project outside that root.

## Share one home between applications

These can run at the same time:

```sh
toby opencode shared --project ~/Projects/frontend
toby codex shared --project ~/Projects/backend
```

The applications share `/toby/home` but use distinct Bubblewrap processes,
project sets, writable root overlays, and run capabilities. Toby-owned
generated files use last-launch-wins behavior for any shared home or tool
volume.

## Run a command

```sh
toby exec my-app -- npm test
```

Everything after the first `--` belongs to the command.

For a script that must receive only the command's foreground output:

```sh
toby --quiet exec my-app -- npm test
```

Use `--debug` instead when diagnosing startup; it exposes raw startup
subprocess output. Terminal debug launches retain OCI progress bars, while
redirected debug output is append-only. The two flags cannot be combined.

## Use a project configuration set

```yaml
# ~/Projects/my-app/.toby/config.yaml
name: my-app
projects:
  my-app:
    primary: true
  shared:
    path: ../shared
tools:
  opencode:
    primary: true
  npm:
workdir: /toby/workspace/my-app
```

```sh
toby --config ~/Projects/my-app/.toby/config.yaml
```

Enable direct-launch autoload:

```yaml
# ~/.config/toby/config.yaml
settings:
  autoloadProjectConfig: true
```

Then:

```sh
toby opencode my-app
```

## Add a local ignored fragment

```yaml
# ~/Projects/my-app/.toby/config.d/99-local.yaml
sandbox:
  pull: never
settings:
  debug: true
```

```gitignore
.toby/config.d/99-local.yaml
```

Fragments are merged lexically after `.toby/config.yaml`. Relative paths in
every fragment still resolve from the project root.

## Overlay a direct launch

Given:

```yaml
# extras.yaml
projects:
  library:
    path: ../library
tools:
  npm:
```

Run:

```sh
toby --config extras.yaml codex my-app -- --model gpt-5
```

Codex and `my-app` remain primary; the file contributes the extra project and
tool.

## Script a launch

```yaml
# test.yaml
name: my-app
projects:
  my-app:
tools:
  exec:
    primary: true
    params: ["npm", "test"]
  npm:
```

```sh
toby --config test.yaml -- -- --watch
```

This runs `npm test -- --watch` in `/toby/workspace/my-app`.

## Add a models API

```yaml
# ~/.config/toby/config.yaml
resources:
  models:
    local:
      protocol: openai
      name: Local models
      url: https://api.example.com/v1
      headers:
        Authorization: "Bearer {env:EXAMPLE_API_KEY}"
```

The application receives a launch-scoped loopback URL and synthetic credential.
The configured URL and real header stay on the host. Toby discovers the model
list through that authorized route and writes it into the application's
generated configuration.

## Add a remote MCP

```yaml
# ~/.config/toby/config.yaml
resources:
  mcps:
    docs:
      type: remote
      transport: http
      url: https://example.com/mcp
      headers:
        Authorization: "Bearer {file:docs-token}"
```

Supported applications see a stdio command equivalent to:

```sh
tobys resource connect -- <client-resource-id>
```

The generated ID is a per-launch UUID. The configured name, upstream URL, and
header remain on the host side of the launch.

## Add a local stdio MCP

```yaml
resources:
  mcps:
    search:
      type: local
      transport: stdio
      image: ghcr.io/example/search-mcp@sha256:...
      command: ["/usr/local/bin/search-mcp"]
      environment:
        SEARCH_TOKEN: "{env:SEARCH_TOKEN}"
      scope: user
      network: host
```

One Bubblewrap sidecar runs for each live MCP connector. It remains alive for
that connector instead of restarting for each tool call.

## Add a reusable local HTTP MCP

```yaml
resources:
  mcps:
    index:
      type: local
      transport: http
      image: ghcr.io/example/index-mcp@sha256:...
      command:
        - /usr/local/bin/index-mcp
        - --socket
        - /run/toby/index.sock
      endpoint:
        kind: unix
        socket: /run/toby/index.sock
        path: /mcp
      scope: home
      network: private
      mounts:
        - source: /srv/index-data
          target: /data
          access: read_only
          scope: home
```

Matching definitions may share the agent-owned process, while each connector
keeps independent Streamable HTTP state. The process stops after all connectors
have been idle for the configured timeout, even if a launch remains registered;
the next connector starts it again through the same lease.

## Configure host Git approval

```yaml
permissions:
  actions:
    git.commit: ask
    git.fetch: allow
    git.push: always-ask
```

The built-in Toby MCP performs these operations in repositories already visible
to the run. The launch process uses host Git identity, credential helpers, SSH
agent, and signing setup without mounting them into the sandbox.

## Run T3 with coding tools

```sh
toby t3 my-app --with-claude --with-codex --with-opencode
```

Toby installs each selected tool and writes its native MCP/instruction
configuration before T3 starts.

## Use the Docker tool

```sh
toby docker my-app -- ps
```

This is an explicit high-trust launch. The `docker` tool creates a private
per-run relay at `/run/toby/docker.sock`; the host Docker socket itself is not
mounted into the sandbox. Normal Toby runtime and other tools do not use or
expose Docker.

## Install or upgrade

```sh
toby opencode my-app --install
toby opencode my-app --upgrade
```

`--install` installs and exits. `--upgrade` forces installation before the
foreground launch.
