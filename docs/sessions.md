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
  signal number when a signal ended it, 127 when it cannot be started).
  Standard input, output and error are passed through separately, and the
  command starts only once Toby is attached to it, so no output is lost.
  When both standard input and output are terminals the command gets a
  terminal of the same size; otherwise interrupt, termination, hangup and
  quit signals sent to `toby` are passed on to the command. If writing the
  command's output fails on the host, `toby` exits with an error.
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
toby sessions kill ID     # hang up and terminate the session; kill it after 3 seconds
```

Reattaching prints the session's recent output (up to 1 MiB) and then asks
full-screen programs to redraw. Attaching from a second terminal detaches the
first.

If the connection to a session is lost (for example while Toby's host or
guest processes restart), Toby reattaches automatically for up to 30 seconds
and continues the output where it stopped. If more than 1 MiB of output was
produced in the meantime, the part that is no longer buffered is lost:
`toby exec` without a terminal then fails with an error, and a terminal
session shows a notice.

When an attachment ends, Toby turns off terminal modes the session left on
(alternate screen, bracketed paste, mouse reporting, hidden cursor, keyboard
protocol levels).
