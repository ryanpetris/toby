# Examples

Worked recipes for common Toby tasks. For the field-level reference see
[configuration.md](configuration.md); for tool specifics see
[tools.md](tools.md).

Throughout, `<env>` is an environment name that maps to a host project and to a
persistent private sandbox home of the same name. By default, host projects are
resolved under the host `XDG_PROJECTS_DIR`. Docker sandboxes show projects under
`/toby/workspace`; Bubblewrap sandboxes keep them under `XDG_PROJECTS_DIR`.

## Launch a coding tool in a project

```sh
mkdir -p ~/Projects/my-app

toby opencode my-app      # OpenCode
toby claude my-app        # Claude Code
toby codex my-app         # Codex
toby copilot my-app       # Copilot
toby grok my-app          # Grok
```

Point an environment at a different project directory:

```sh
toby claude review-env --project ~/Projects/customer-api
```

## Run a one-off command in the sandbox

```sh
toby exec my-app -- npm test
toby exec my-app -- bash -lc 'go build ./...'
```

## Run T3 Code with one or more coding tools

T3 Code is a launcher that can drive the other coding tools. Use
`--with-<tool>` to install and wire up each tool you want available inside t3:

```sh
# t3 with a single coding tool
toby t3 my-app --with-claude

# t3 with several coding tools available
toby t3 my-app --with-claude --with-codex --with-opencode
```

Each enabled tool is installed into the sandbox home and gets its Toby
integration config generated (the `git.*` MCP server, any configured providers,
and your instruction files), so it works the moment you select it inside t3.

Pass extra arguments through to t3 after `--`:

```sh
toby t3 my-app --with-claude -- <t3 arguments>
```

Or define it declaratively with t3 as the primary tool:

```yaml
# t3.yaml
projects:
  my-app:
tools:
  t3:
    primary: true
  claude:
  codex:
  opencode:
```

```sh
toby --config t3.yaml
```

## Use a launch config

A launch config describes one launch's sandbox, projects, and tools.

```yaml
# review.yaml
sandbox:
  name: review
  runtime:
    default: docker
    docker:
      image: node:lts-bookworm
projects:
  app:
    primary: true
  shared:
    path: ../shared-lib
tools:
  opencode:
    primary: true
    params: ["--model", "anthropic/claude-sonnet-4-5"]
  uv:
  npm:
```

```sh
toby --config review.yaml
```

The primary tool (`opencode`) launches in the foreground; `uv` and `npm` are
installed and available. The primary project (`app`) is the working
directory. In this Docker example, both projects appear inside the sandbox under
`/toby/workspace/`.

### Overlay a config onto a direct launch

```sh
toby --config review.yaml opencode my-app
```

Here the CLI tool (`opencode`) and project (`my-app`) stay foreground and
primary; the config contributes sandbox settings plus the additional tools and
projects.

## Run a project script through `exec`

```yaml
# test.yaml
projects:
  my-app:
tools:
  exec:
    primary: true
    params: ["npm", "test"]
  npm:
```

```sh
toby --config test.yaml             # runs: npm test
toby --config test.yaml -- -- --watch   # runs: npm test -- --watch
```

Everything after the first `--` (including later `--` tokens) is appended to the
primary tool's `params`.

## Add a model provider

Keep the upstream URL and credentials on the host in your host config; Toby
proxies them to tools through a per-run URL.

```yaml
# ~/.config/toby/config.yaml
providers:
  local:
    type: openai
    baseURL: https://api.example.com/v1
    headers:
      Authorization: "Bearer {env:EXAMPLE_API_KEY}"
    models:
      example-model: {}
```

```sh
EXAMPLE_API_KEY=sk-... toby opencode my-app
```

## Add an MCP server

```yaml
# ~/.config/toby/config.yaml
mcps:
  docs:
    type: remote
    url: https://example.com/mcp
    headers:
      Authorization: "Bearer {env:DOCS_TOKEN}"
```

```sh
DOCS_TOKEN=... toby opencode my-app
```

The `docs` server is exposed to supported tools through a `/proxy/<uuid>` URL.
Toby resolves the `{env:DOCS_TOKEN}` substitution on the host and applies the
header there, so the token never enters the sandbox. (`{file:path}` reads a
secret from a host file the same way.) Toby's built-in `toby` MCP server (the
`git.*` tools) is always injected as well.

## Commit, push, and tag with host credentials

Host SSH keys and GPG setup are not mounted into the sandbox. Use the Toby Git
MCP tools, available to agents automatically, so the operation runs on the host
with your real credentials:

```text
# through the Toby MCP server
git.commit(repository: "my-app", message: "Fix bug")
git.push(repository: "my-app", branch: "main")
git.tag(repository: "my-app", tag: "v1.2.3", message: "Release 1.2.3")
git.fetch(repository: "my-app")
git.rebase(repository: "my-app", base: "origin/main")
```

Repository names are relative to the sandbox project root and must already be visible
in the sandbox. `git.commit` commits only already-staged files; it does not add
files. See [sandbox.md](sandbox.md#mcp) for the full tool reference.

## Use Host-Backed Mounts

By default each environment uses provider-backed managed mounts. To share a
tool's data from a host directory (for example, to reuse an existing OpenCode
login), enable host backing for that tool's managed mounts:

```yaml
# ~/.config/toby/config.yaml
mountProfiles:
  default:
    backing: provider
  host-state:
    backing: host
    hostRoot: ~/.config/toby/mounts/opencode
settings:
  suppressWarnings:
    - mount.host-backing
tools:
  opencode:
    mountProfile: host-state
```

Running multiple sandboxes against the same host-backed managed mount can
corrupt that tool's databases, which is why Toby emits `mount.host-backing`
unless you suppress it.

## Build a custom Docker image for the sandbox

```yaml
# build.yaml
sandbox:
  runtime:
    default: docker
    docker:
      build:
        context: .
        dockerfile: Dockerfile.toby
projects:
  my-app:
tools:
  opencode:
    primary: true
```

```sh
toby --config build.yaml
```

The image is built from `Dockerfile.toby` before launch. The image must contain
the runtime dependencies for your selected tools, including `curl` for the
helper bootstrap. The repository's own [`support/toby/Dockerfile`](../support/toby/Dockerfile)
is a working example.

## Install or upgrade a tool without launching it

```sh
toby claude my-app --install    # install Claude Code, then exit
toby claude my-app --upgrade    # reinstall Claude Code, then launch
```
