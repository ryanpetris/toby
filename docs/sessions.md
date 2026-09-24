# Sessions

A session is a process tree started inside a machine: a tool, a shell or a
command. Sessions keep running when the terminal that started them closes,
and can be reattached later.

## Starting sessions

```sh
toby exec [--as-root] [--machine ID] [--cwd DIR] -- COMMAND [ARGS...]
toby shell [--as-root] [--machine ID]
```

- `toby exec` runs a command and exits with its exit status (128 plus the
  signal number when a signal ended it). Standard input, output and error are
  passed through; when both standard input and output are terminals the
  command gets a terminal of the same size.
- `toby shell` opens a login shell.
- `--as-root` runs as root in the guest instead of the home's user.
- `--machine` selects a running machine by ID. Without it, Toby uses the only
  running machine.

## Detaching and reattaching

Press `Ctrl-\` and then `d` to detach; the session keeps running. Press
`Ctrl-\` twice to send a single `Ctrl-\` to the session.

```sh
toby sessions ls          # sessions in every running machine
toby attach [SESSION]     # reattach; without an ID, the only detached session
toby sessions kill ID     # send SIGTERM to the session
```

Reattaching prints the session's recent output (up to 1 MiB) and then asks
full-screen programs to redraw. Attaching from a second terminal detaches the
first.

If the connection to a session is lost (for example while Toby's host or
guest processes restart), Toby reattaches automatically for up to 30 seconds.
Output produced while disconnected is not shown again.

When an attachment ends, Toby turns off terminal modes the session left on
(alternate screen, bracketed paste, mouse reporting, hidden cursor, keyboard
protocol levels).
