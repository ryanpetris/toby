# Sharing files with a machine

`toby mount` makes a host directory visible in a running machine. The
directory is shared, not copied: changes on either side are visible on the
other right away.

```sh
toby mount ~/src/project                  # at /toby/workspace/project
toby mount ~/data --at /srv/data --ro     # somewhere else, read-only
toby unmount ~/src/project                # by host path, machine path or ID
```

`toby mount` prints where the directory appears in the machine. Mounting
works while sessions are running; they see the new directory immediately.
It uses the machine of your default home and root, started if needed;
choose another with `--home` and `--root`, or `--machine`.

`toby unmount` takes the host path, the path in the machine or the
attachment ID. It is refused while something in the machine still uses the
directory (a shell whose working directory is inside it, an open file). A
mount point Toby created stays behind as an empty directory only root can
write to, so programs that still expect the directory fail instead of
writing into the machine's root or home. Only empty directories owned by
root are changed that way (as Toby creates them); others, such as a
directory in your home, keep their owner and permissions.

Mounts end when the machine stops. `toby mount --persist` records the
mount so it is made again every time the machine starts.

## Ownership and permissions

- Files the host user owns appear owned by the machine's user (the home's
  user, or root in a machine without a home). Files owned by anyone else
  appear owned by `nobody` (65534).
- Everything written through a mount, by the machine's user or by root, is
  written as the host user: new files on the host belong to you.
- `chown` to the machine's user or to root succeeds and changes nothing;
  other owners are refused.
- setuid and setgid bits are removed, device files and FIFOs cannot be
  created, and the mount does not honour setuid bits or device files.
- A read-only mount refuses every write, even from root in the machine.

## What the machine can reach

Only the mounted directory and what is below it. Symbolic links are passed
to the machine as links and resolved there, inside the machine, so a link
pointing outside the directory does not reach the host's files. Names such
as `..` cannot climb above the mounted directory.
