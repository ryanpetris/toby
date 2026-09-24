# Tools and models

## Running a tool

```sh
toby claude                      # Claude Code, in the current directory
toby codex --project ~/src/app   # Codex, with another project
toby opencode -- --help          # arguments after -- go to the tool
```

`toby <tool>` runs the tool in the machine of your default home and root
(`--home`, `--root` or `--machine` choose another). On first use it creates
what is missing: the home (for your user), the root named `default` and,
before that, the default image. It installs the tool in the home if its
check fails, then starts it with the project attached at
`/toby/workspace/<name>`, in the directory you ran it from. Projects stay
attached while a session of the tool uses them.

| Option | Effect |
| --- | --- |
| `--project PATH` | attach PATH instead of the current project (repeatable; the first is the working directory) |
| `--install` | install the tool if needed (with `--upgrade`, update it), then exit |
| `--upgrade` | run the tool's updater before starting it |
| `--attach` | reattach a running session of the tool instead of starting one |
| `--new` | start a session without asking about a detached one |
| `--yolo` | start the tool without its permission prompts (also `settings.yolo = true`) |
| `--ephemeral` | run the machine on a throwaway layer over the root |

If a session of the tool is running detached in that machine, `toby
<tool>` asks whether to attach to it.

### Projects

Projects live in `settings.projects_dir` (default `~/Projects`). The
current project is the nearest directory with `.git` or `.toby` above the
current directory, or else the directory in `projects_dir` it is in. A
relative `--project` starts in `projects_dir` unless it starts with `./` or
`../`. Projects outside `projects_dir` need
`settings.allow_external_projects = true`.

### Launch files

A launch file sets up one launch; `toby run -f review.toml` starts it
(with the same options as `toby <tool>`, and arguments after `--`).

```toml
tool = "claude"
tools = ["github_cli"]            # also prepared, and on the tool's PATH
params = ["--model", "opus"]
home = "work"
root = "work"
image = { dockerfile = "Dockerfile", context = "." }   # for a root that does not exist yet
workdir = "src"                   # in the primary project, or an absolute path
forwards = [{ host = 3000 }]      # host 127.0.0.1:3000 to the same port in the machine
mcp = ["github"]                  # configured MCP servers the tool gets, while the launch runs
cpus = 4
memory = "8G"

[projects.app]                    # path defaults to <projects_dir>/app
primary = true
[projects.library]
path = "../library"               # relative to the launch file; may be anywhere

[settings]
yolo = true
```

`image` is `"default"`, an image ID, or `{ mkosi = DIR }`, `{ dockerfile =
FILE, context = DIR }`, `{ registry = REF }` or `{ archive = FILE }`;
`[defaults] image` sets it for every new root, and `[defaults] cpus` and
`memory` the size of every machine.

### Project configuration

A project can carry `.toby/config.toml` with the same keys except `tool`,
`tools`, `params` and `[settings]`; its project paths start in the project
and must stay in `projects_dir`. Toby reads it only with
`settings.autoload_project_config = true`, because a cloned repository
could otherwise enable your MCP servers or open forwards; its image's
files (Dockerfile, build context, mkosi directory, archive) must be in the
project itself, since the build can reach the network; a launch file can
name others. Options win over a launch file, which
wins over the project configuration, which wins over
`~/.config/toby/config.toml` (so a launch file's `yolo = false` turns off
a global `settings.yolo`).

### Configuration

`~/.config/toby/config.toml` holds the settings on this page. `toby config
get KEY` prints one and `toby config set KEY VALUE` changes one, keeping
the rest of the file as it is:

```sh
toby config set settings.projects_dir "~/src"
toby config set tools.claude.params '["--model", "opus"]'
```

Tools are installed in the home, so every root of that home finds them.
Updating a tool does not affect sessions that are already running.

Signing in happens inside the tool, as usual. Claude Code shows a link to
open on the host and asks for the code it returns. Codex's sign-in calls
back to a port on the host, which Toby forwards to the machine while a
Codex session runs.

## Built-in tools

| Tool | Also found as | Installed with | Needs in the image |
| --- | --- | --- | --- |
| `claude` | | the Claude Code install script | `bash`, `curl` |
| `codex` | | npm, into `~/.local` | `bash`, `npm` |
| `opencode` | | the OpenCode install script | `bash`, `curl` |
| `copilot` | | npm, into `~/.local` | `bash`, `npm` |
| `cursor` | `cursor-agent` | the Cursor CLI package | `bash`, `curl`, `tar` |
| `grok` | | the Grok CLI release | `bash`, `curl` |
| `dcode` (Deep Agents Code) | | `uv tool install` | what `uv` needs |
| `speckit` (Spec Kit) | `specify` | `uv tool install` from the latest release | `curl`, `git` |
| `t3` (T3 Code) | | npm, into `~/.local` | `bash`, `npm`, `make`, `g++`, `python3` |
| `uv` | `uvx` | the uv install script | `sh`, `curl` |
| `npm` | | the image | `npm` |
| `github_cli` | `gh` | the latest release | `sh`, `curl`, `tar` |
| `gitlab_cli` | `glab` | the latest release | `sh`, `curl`, `tar` |
| `fj` (Forgejo CLI) | | the latest release | `sh`, `curl`, `tar` |
| `exec` | | nothing | nothing |

`toby gh`, for example, runs `github_cli`. `dcode` and `speckit` install
`uv` first and run with it on `PATH`. `npm` runs the image's npm with
global packages in `~/.local/npm-global`. `exec` runs its parameters as a
command, or a login shell without any; it is meant for launch files, as
`toby exec` runs a command directly.

A tool fails to start with a message naming the missing commands if the
image lacks them; add them to the image.

### Instructions and permissions

```toml
instructions = ["~/AGENTS.md", "~/instructions/*.md"]

[permissions.paths]
"~/notes" = "allow"
"/srv/data" = "deny"
```

`instructions` are files on the host (a `*` in the last part of the path
matches several), read at each launch and written, joined, into each
tool's own instructions file in the home: `~/.claude/CLAUDE.md`,
`~/.codex/AGENTS.md`, `~/.config/opencode/AGENTS.md`,
`~/.copilot/copilot-instructions.md`, `~/.cursor/rules/toby.mdc`,
`~/.grok/AGENTS.md` and the `toby` agent of Deep Agents Code. A missing
file is reported as `config.instruction-missing`.

`permissions.paths` are paths in the machine (`~` is the home there) that
Claude Code, Codex and OpenCode may use outside the project, or may not.
`/tmp` and the launch's projects are always allowed, and `--yolo` allows
`/`. Codex also trusts the projects, so it does not ask about each.

## Your own tools

A file in `~/.config/toby/tools/<name>.toml` defines a tool or replaces a
built-in one:

```toml
[tool]
name = "mytool"
aliases = ["mt"]                             # toby mt runs it too
depends = ["uv"]                             # prepared first, and on PATH
requires = ["bash", "curl"]                 # commands the image must have
check = ["mytool", "--version"]              # succeeds when installed
install = { script = "curl -fsSL https://example.com/install.sh | bash" }
update = { script = "mytool update" }        # optional; default: install
launch = ["mytool"]
yolo = ["--no-confirm"]                      # added by --yolo
path = ["~/.mytool/bin"]                     # added to PATH (~/.local/bin always is)
env = { MYTOOL_THEME = "dark" }

[tool.models]                                # used when a provider is configured
protocols = ["openai"]                       # the APIs the tool speaks
env = { OPENAI_BASE_URL = "{{ models.url }}/v1", OPENAI_API_KEY = "{{ models.token }}" }

[[tool.files]]                               # written into the home before launch
path = "~/.config/mytool/settings.json"
format = "json"                              # json, toml or text
mode = "merge"                               # merge into the file, extend (lists too), or replace it
template = """{ "telemetry": false }"""
optional = true                              # skipped when the template renders empty

[[tool.forwards]]                            # while a session of the tool runs
when = "login"
direction = "host-to-guest"
port = 8123
```

Templates are Jinja templates. They can use `user`, `home`, `workspace`
(the primary project's path in the machine), `projects`, `instructions`,
`permissions` (path to `allow` or `deny`), `allowed` and `denied` (those
without wildcards), `yolo`, and, when a provider is configured for the
tool, `models.url`, `models.token`, `models.provider`, `models.protocol`
and `models.list` (the provider's models, when it lists them).

## Models

Tools can use model providers through Toby instead of their own sign-in.
The provider's credentials stay on the host: the machine only gets a token
that works with Toby's proxy for that machine, and the proxy adds the real
credentials.

```toml
[models.anthropic]
protocol = "anthropic"
url = "https://api.anthropic.com"
headers = { "x-api-key" = "{file:keys/anthropic}" }

[tools.claude]
models = "anthropic"
params = ["--model", "opus"]                 # extra arguments of every launch
```

`{file:path}` is the trimmed content of a file (relative to
`~/.config/toby/`, `~` allowed) and `{env:NAME}` an environment variable
of the daemon; both are read when used. In the machine, the provider is at
`http://127.0.0.1:41100/<provider>`.

When it prepares a tool, Toby asks the provider for its models (cached
for five minutes) and writes them into tools that list models, such as
OpenCode. A provider that does not answer is reported as
`models.endpoint-unavailable`.

## MCP servers

```toml
[mcp.github]                                 # a local server with a secret
kind = "stdio"
command = ["npx", "-y", "@modelcontextprotocol/server-github"]
env = { GITHUB_TOKEN = "{file:keys/github}" }

[mcp.docs]                                   # a remote server
kind = "http"
url = "https://example.com/mcp"
headers = { Authorization = "Bearer {env:DOCS_TOKEN}" }

[tools.claude]
mcp = ["github", "docs"]
```

Toby writes the servers a tool's `mcp` list names, and its own server
`toby`, into the tool's configuration when it prepares the tool. A machine
reaches only the servers listed for the tools started in it, and those a
launch names while its session runs.

- A `stdio` server whose command or environment uses `{file:}` or `{env:}`
  runs isolated: in a small machine of its own (`toby mcp ls` shows it),
  started when a tool connects and stopped after five idle minutes. Its
  secrets exist only in that server's environment; the tool's machine
  never has them. `toby mcp logs` shows what the server writes to its
  standard error. `placement = "machine"` runs a server without secrets
  in the tool's machine instead.
- An `http` server is called through Toby's proxy, which adds its headers;
  the credentials stay on the host.
- An `http` server with a `command` instead of a `url` is one Toby runs
  in a machine of its own, started when a tool first calls it and stopped
  after five minutes without calls; `port` is where it listens in that
  machine.

A server in a machine of its own runs from the default image, or from
`image` (the same forms as a launch's `image`; build it with `toby image
prepare --mcp NAME`). Its machine can reach ports of the host's
127.0.0.1 listed in `host_ports`, at the same port of its own 127.0.0.1:

```toml
[mcp.search]
kind = "http"
command = ["search-server", "--listen", "127.0.0.1:8080"]
port = 8080
image = { dockerfile = "search/Dockerfile" }
host_ports = [5432]                          # a database on the host
```

```sh
toby mcp ls
toby mcp logs github [-f]
toby mcp restart github
```

## Toby's MCP server and approvals

Toby's own MCP server lets a tool use your git credentials:
`git_fetch` fetches the branches of a remote configured in the project's
repository, and `git_push` pushes a branch to it. `forward_request` asks
for a port forward while the machine's sessions run, and `session_info`
describes the machine.

The machine can write the project's repository, so git never runs in it:
Toby fetches and pushes through a private repository of its own, over
`https` and `ssh` only, to the URL the approval shows (and where your git
config's `insteadOf` rules send it), and writes the fetched objects and
the remote-tracking branches back into the project. They do not run while
your git config is inside a mounted project.
Hooks in the project do not run. Projects mounted read-only allow only
pushes. The project's objects are linked into that repository, or copied
when the project is on another file system than Toby's state directory,
which makes each fetch and push of a large project on another file system
slower.

Actions that need approval wait until you decide. The approval opens over
the attached session (see [Sessions](sessions.md)); you can also answer
from any terminal:

```sh
toby approvals                               # pending first
toby approvals ID approve                    # or deny; shows the action and asks first
```

An approval nobody decides expires after ten minutes, and when the daemon
restarts. A daemon restart also ends the tools' connections to Toby's
server; tools started afterwards connect again.

`[permissions.actions]` sets the policy per action: `allow`, `deny`,
`ask` (skipped when a tool in the machine runs with `--yolo`), or
`always-ask`. The actions are `git.fetch`, `git.push`, `forward` and
`session.info`. Without configuration, session information is allowed,
push always asks, and the others ask.

```toml
[permissions.actions]
"git.fetch" = "allow"
"git.push" = "always-ask"
```
