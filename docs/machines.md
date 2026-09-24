# Machines and the daemon

A machine is a virtual machine for one home and one root. It starts when a
command needs it, runs your sessions, and stops on its own after it has
been idle.

## Machines

```sh
toby exec -- make test            # in the machine of your default home and root
toby shell --home work --root ci  # another pair
toby machine ls
toby machine stop ID              # or --all
toby machine logs ID [-f]
```

Without `--home` and `--root`, Toby uses the home named in
`defaults.home` (`default` unless configured) and that home's default root,
or the root named `default`. The first command for a pair creates its
machine; later commands join the running machine. `--machine ID` selects a
running machine directly.

A home and a root each belong to at most one running machine. When a
command asks for a pair whose home or root another running machine uses,
it fails and names that machine; stop it with `toby machine stop` or choose
another home or root.

A machine with no sessions and nothing mounted with `--persist` or
`toby mount` stops after `daemon.idle_timeout` (15 minutes by default;
`"0"` keeps machines running). Detached sessions keep a machine running.

## The daemon

`tobyd` keeps track of machines, builds images and starts and stops
machines. Commands start it when it is not running. Restarting it does not
affect running machines or sessions: session output and input go directly
to the machine.

```sh
toby daemon status
toby daemon restart
toby daemon logs [-f]
toby doctor                       # checks KVM, bundled programs, the back end and the daemon
```

## Back ends

`daemon.backend` chooses how machines are run:

- `systemd-user` (the default): the daemon and every machine's processes
  are units of your systemd user instance (`tobyd.socket`,
  `toby-vm@ID.service` and its parts), with logs in the journal. Without
  linger, your user instance, and with it every machine, stops shortly
  after your last login session ends; `toby linger on` keeps them running.
  Toby warns about this once per daemon start; add
  `"daemon.linger-disabled"` to `settings.suppress_warnings` to silence it.
- `direct`: the daemon and machines run as detached processes, with logs
  in `~/.local/state/toby/logs/`. They survive logout only when logind is
  configured with `KillUserProcesses=no` (the default on most
  distributions); `toby daemon status` shows the setting.

Stop all machines before switching back ends.
