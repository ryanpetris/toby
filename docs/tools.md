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
`/toby/workspace/<name>` as its working directory. Projects stay attached
while a session of the tool uses them.

| Option | Effect |
| --- | --- |
| `--project PATH` | attach PATH instead of the current directory (repeatable; the first is the working directory) |
| `--install` | install the tool if needed (with `--upgrade`, update it), then exit |
| `--upgrade` | run the tool's updater before starting it |
| `--attach` | reattach a running session of the tool instead of starting one (`--new`, the default, starts one) |
| `--yolo` | start the tool without its permission prompts (also `settings.yolo = true`) |
| `--ephemeral` | run the machine on a throwaway layer over the root |

Tools are installed in the home, so every root of that home finds them.
Updating a tool does not affect sessions that are already running.

Signing in happens inside the tool, as usual. Claude Code shows a link to
open on the host and asks for the code it returns. Codex's sign-in calls
back to a port on the host, which Toby forwards to the machine while a
Codex session runs.

## Built-in tools

| Tool | Installed with | Needs in the image |
| --- | --- | --- |
| `claude` | the Claude Code install script | `bash`, `curl` |
| `codex` | npm, into `~/.local` | `bash`, `npm` |
| `opencode` | the OpenCode install script | `bash`, `curl` |

A tool fails to start with a message naming the missing commands if the
image lacks them; add them to the image.

## Your own tools

A file in `~/.config/toby/tools/<name>.toml` defines a tool or replaces a
built-in one:

```toml
[tool]
name = "mytool"
requires = ["bash", "curl"]                 # commands the image must have
check = ["mytool", "--version"]              # succeeds when installed
install = { script = "curl -fsSL https://example.com/install.sh | bash" }
update = { script = "mytool update" }        # optional; default: install
launch = ["mytool"]
yolo = ["--no-confirm"]                      # added by --yolo
path = ["~/.mytool/bin"]                     # added to PATH (~/.local/bin always is)
env = { MYTOOL_THEME = "dark" }

[tool.models]                                # used when a provider is configured
protocol = "openai"
env = { OPENAI_BASE_URL = "{{ models.url }}/v1", OPENAI_API_KEY = "{{ models.token }}" }

[[tool.files]]                               # written into the home before launch
path = "~/.config/mytool/settings.json"
format = "json"                              # json, toml or text
mode = "merge"                               # merge into the file, or replace it
template = """{ "telemetry": false }"""

[[tool.forwards]]                            # while a session of the tool runs
when = "login"
direction = "host-to-guest"
port = 8123
```

Templates are Jinja templates; they can use `models.url` and
`models.token` (only when a provider is configured for the tool), `user`
and `home`; `env` values can also use `workspace`, the project's path in
the machine.

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
reaches only the servers listed for the tools started in it.

- A `stdio` server whose command or environment uses `{file:}` or `{env:}`
  runs isolated: in a small machine of its own (`toby mcp ls` shows it),
  started when a tool connects and stopped after five idle minutes. Its
  secrets exist only in that server's environment; the tool's machine
  never has them. `toby mcp logs` shows what the server writes to its
  standard error. `placement = "machine"` runs a server without secrets
  in the tool's machine instead.
- An `http` server is called through Toby's proxy, which adds its headers;
  the credentials stay on the host.

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
`https` and `ssh` only, to the URL the approval shows, and writes the
fetched objects and the remote-tracking branches back into the project.
Hooks in the project do not run. Projects mounted read-only allow only
pushes. The project's objects are linked into that repository, or copied
when the project is on another file system than Toby's state directory,
which makes each fetch and push of a large project on another file system
slower.

Actions that need approval wait until you decide. A notice appears in the
attached session; answer with:

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
