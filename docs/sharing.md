# Sharing files with a machine

`toby mount` makes a host directory visible in a running machine. The
directory is shared, not copied: changes on either side are visible on the
other right away.

```sh
toby mount ~/src/project                  # at /toby/workspace/project
toby mount ~/data --at /srv/data --ro     # somewhere else, read-only
toby unmount ~/src/project                # by host path, or by attachment ID
```

`toby mount` prints where the directory appears in the machine. Mounting
works while sessions are running; they see the new directory immediately.
With several machines running, choose one with `--machine`.

`toby unmount` is refused while something in the machine still uses the
directory (a shell whose working directory is inside it, an open file).
The mount point stays behind as an empty directory nobody can write to, so
programs that still expect the directory fail instead of writing into the
machine's root.

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
