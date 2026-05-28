# Toby

Toby Sandbox runs development tools inside private-home Bubblewrap environments.

## Usage

```sh
toby <tool> <env> -- <tool arguments>
toby exec <env> -- <command arguments>
```

Use `--tmp-env` for a temporary sandbox home and `--print` to print the generated `bwrap` command instead of running it.

## Environment

- `XDG_PROJECTS_DIR` defaults to `~/Projects`.
- `XDG_CACHE_HOME` defaults to `~/.cache`; sandbox homes are stored under `$XDG_CACHE_HOME/toby/sandboxes`.
- `TOBY_SANDBOX_ROOT` overrides the sandbox home root when set.
