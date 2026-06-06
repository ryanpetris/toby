# Debugging Sandbox Startup

This page is a runbook for diagnosing failures in the sandbox bring-up path — the
sequence that creates the container, delivers the `toby` binary, starts the
in-sandbox manager, and establishes the gRPC-over-stdio control link before the
requested tool runs. For how that path is built, see
[architecture.md](architecture.md) and [sandbox.md](sandbox.md).

## How startup works (and how it fails)

The host launches one container, `docker cp`s the `toby` binary into it, and starts
it on the idle `toby sandbox idle` main process, then runs `toby sandbox manager` as
a `docker exec`. The manager exec's stdin/stdout *are* a single gRPC connection (no
TTY): the manager is the gRPC client, the host is the server. The
manager binds a fixed loopback proxy listener (`tunnel.ProxyAddr`,
`127.77.0.1:47600`) and calls `Ready`. The host waits for that `Ready` for up to 30s
before proceeding to mount-init, context files, tool install, and launch.

Two failure shapes to distinguish from the host error message:

- **`timed out waiting for sandbox manager to start`** — the container is alive but
  the gRPC handshake never completed. Causes: the manager exec wrote non-gRPC bytes
  to fd 1 (stdout must carry only gRPC frames; all logging must go to stderr), or the
  manager never bound its listener.
- **`sandbox manager exited before reporting ready`** — the manager exec died
  (bad/missing binary, a crash on startup, a config error). The gRPC `Serve` loop saw
  EOF rather than timing out.

## What you need on the host

- **Docker access.** A reachable Docker-compatible daemon (`docker ps` works). Toby
  talks to it via the SDK and honors `DOCKER_HOST` / the active context.
- **A test project** under `$XDG_PROJECTS_DIR` (default `~/Projects`). A throwaway
  directory with a single file is enough (e.g. `~/Projects/test/HELLO.md`). The
  environment name (`test`) maps to `$XDG_PROJECTS_DIR/test`.
- **The `exec` tool as the smallest end-to-end probe.** `toby exec <env> -- <cmd>`
  exercises the whole bring-up without needing tool credentials. If a non-interactive
  command returns, every layer (create → cp → manager → mount-init → docker exec)
  works.

### Running Toby from inside a Toby sandbox (dogfooding)

When you build and run `toby` *inside* a Toby sandbox (common when developing Toby
on itself), the inner process sees projects under `/toby/workspace`, but the Docker
daemon is the host's, so bind-mount sources are resolved against the **host**
filesystem (`/home/<you>/Projects/...`), not the sandbox view. The inner binary also
validates project paths against its own (sandbox) filesystem. You must reconcile the
two.

The trick is a symlink so the host project path also resolves inside the sandbox,
combined with pointing `XDG_PROJECTS_DIR` at the host path. Project resolution uses
`filepath.Abs` (not `EvalSymlinks`), so the recorded bind source stays the host path
while `os.Stat` follows the link:

```sh
# Inside the sandbox you run as a non-root uid, and / is root-owned, so create the
# mapping as root via docker exec into your own container. Find your container with
# its labels (toby.sandbox=<name>, toby.phase=run); confirm identity with a marker
# file visible from both sides (e.g. touch a file in the repo and ls it via exec).
docker exec -u 0 <my-container-id> sh -c \
  'mkdir -p /home/<you> && ln -sfn /toby/workspace /home/<you>/Projects'

# Then run with the host projects path. The bind source becomes
# /home/<you>/Projects/test (host-resolvable); os.Stat follows the symlink to
# /toby/workspace/test (sandbox-resolvable).
XDG_PROJECTS_DIR=/home/<you>/Projects toby exec test -- sh -c 'echo OK; id; pwd'
```

`<my-container-id>` is the sandbox you are running in. There is no `hostname`
binary and cgroup v2 hides the id, so identify it by label and confirm with a marker
file rather than guessing.

## Debugging steps

1. **Build a stable binary** rather than `go run` each time:
   `go build -o /tmp/toby .`. The binary `toby` copies into the sandbox is its own
   `/proc/self/exe`, so the built binary is what runs inside the container.
2. **Probe with `exec`, non-interactively, with a timeout**, capturing both streams:
   ```sh
   timeout 150 env XDG_PROJECTS_DIR=/home/<you>/Projects \
     /tmp/toby exec test -- sh -c 'echo OK; id; pwd; ls -la' </dev/null > /tmp/run.log 2>&1
   echo "exit=$?"; cat /tmp/run.log
   ```
   The manager's own stderr (container fd 2) is demultiplexed and forwarded to the
   host stderr, so manager-side errors land in this log.
3. **Read the error shape** (see above) to decide between a lost/garbled gRPC
   handshake (timeout) and a dead process (exited-before-ready).
4. **Inspect the run container** while it is up (or with `--debug`, which leaves it
   running):
   ```sh
   docker ps --no-trunc --format '{{.ID}}  {{.Command}}  {{.Names}}'
   docker inspect <id> --format '{{json .Config.Entrypoint}} {{json .Config.Cmd}}'
   # Expect entrypoint = ["/toby/bin/toby"], cmd = ["sandbox","manager"], no TTY.
   ```
   Identify which container is yours by its labels — the run container carries
   `toby.sandbox=<env>`, `toby.phase=run`; MCP sidecars carry `toby.mcp=<server>`.
5. **Do not kill the outer session.** When dogfooding, the container you live in is
   labeled with the outer session's `toby.sandbox` name; leave it and its sidecars
   alone. Scope any cleanup to your inner test runs by label, e.g.
   `docker ps -aq --filter label=toby.sandbox=test`.
6. **Verify the proxy tunnel end-to-end** by driving a real request (needs provider
   credentials on the host): `toby claude test -- --print "say hello"`. A response
   confirms traffic flowed container → `127.77.0.1:47600` → manager tunnel → gRPC
   stdio → host reverse proxy (credentials injected) → upstream → back. A
   `connection refused` to the loopback address means the tunnel is broken; an
   auth/network error from the upstream means it reached the host proxy fine.

## Things that bite

- **Manager runs as a docker exec.** The container's main process is the idle `toby
  sandbox idle`, so `docker logs` stays empty; the manager runs as a `docker exec`
  whose stdout carries the gRPC link. Attaching to the exec returns its stream from
  the first byte, so the preface is captured without an attach-before-start race.
- **Keep fd 1 pure.** Only gRPC frames may go to stdout; route all manager logging
  to stderr, which the host demultiplexes (`stdcopy`) and forwards.
- **No TTY on the control link.** The manager's stdio is binary gRPC; a PTY would
  translate bytes and corrupt frames. The user's interactive tool gets its own TTY
  via a separate `docker exec`.
- **Benign tunnel logs.** `tunnel forward: ... connection reset by peer` (or EOF /
  closed) is a normal keep-alive client close, not an error; it is filtered from the
  manager log.
- **Cleanup is automatic** on a clean or errored return (the run container is
  stopped and removed), except under `--debug`, where it is left running for
  inspection.
