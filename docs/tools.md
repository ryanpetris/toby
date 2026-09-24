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
| `--install` | install or update the tool, then exit |
| `--upgrade` | run the tool's updater before starting it |
| `--attach` | reattach a running session of the tool instead of starting one |
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

Templates are Jinja templates; they can use `models.url`,
`models.token` (only when a provider is configured for the tool), `user`,
`home` and `workspace`.

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
