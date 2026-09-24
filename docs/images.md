# Images, roots and homes

A Toby machine boots from a **root**, a writable disk created from an
**image**, and mounts a **home**, a disk that holds the user's home
directory. Roots can be reset or moved to a newer image at any time; homes
are kept across roots.

## Images

Toby does not download prebuilt images. Every image is built locally in a
builder machine and ends up with the same layout: a root filesystem disk,
the kernel and an initramfs. An image can be built from:

| Source | Command |
| --- | --- |
| The bundled default configuration | `toby image prepare --default` |
| A Dockerfile | `toby image build [--dockerfile FILE] [--context DIR]` |
| An mkosi configuration | `toby image build --mkosi DIR` |
| A registry image | `toby image pull REFERENCE` |
| An OCI archive | `toby image import FILE` |

`toby image ls` lists images with their source, kernel and the roots that
use them. `toby image rm ID` removes an image no root uses, and
`toby image prune` removes every image that is neither used by a root nor
the newest build of its source, and the build caches of sources no image
comes from any more.

Each source has its own build cache (container layers, mkosi caches), so
one source's build never affects another's. Builds of the same source run
one after another.

Build output streams to the terminal and is kept in
`~/.local/state/toby/builds/`.

### The default image

The default image is Debian 13 with the common tools agents expect
(curl, git, bash, Python, Node.js and so on). It also boots the builder
machines and formats homes, so the first build or home creation builds it
first. `toby image prepare --default` builds it when it is missing or
when the bundled configuration or Toby's boot adaptation has changed, and
does nothing otherwise; `--rebuild` forces a rebuild.

`toby image prepare` builds what the configuration will need, skipping
images that are up to date:

| Option | Builds |
| --- | --- |
| (none) | the default image, every MCP server's image, and the current project's |
| `--default` | the default image |
| `--mcp [NAME…]` | the images of MCP servers that run in machines of their own (`[mcp.<name>].image`), or of the named ones |
| `--project [PATH]` | the image of the current project, or of PATH: its `.toby/config.toml` (when `settings.autoload_project_config` is set) or `[defaults] image` |
| `--all` | all of the above for every MCP server, and the source of every root |
| `--rebuild` | also images that are up to date |
| `--pull` | also images from registries and Dockerfiles, fetching their base images again |

The very first default image needs a builder that does not exist yet, so
Toby starts from the Debian 13 cloud image instead:

```sh
toby builder bootstrap            # downloads and verifies the cloud image
toby builder bootstrap --base FILE  # or uses a cloud image you provide
toby builder bootstrap --clean    # deletes the downloaded cloud image
```

`toby builder status` shows the current default image and whether the
cloud image is still on disk.

### Building from a Dockerfile

Existing Dockerfiles work unchanged. The build runs with the context
directory attached read-only, with network access and a build cache that
is kept between builds:

```sh
toby image build --context .toby
```

After the container build, Toby adapts the result so it boots (see
[Custom images](#custom-images)).

### Building with mkosi

mkosi is bundled with Toby, so every build uses the version Toby was
tested with. Toby always builds a `directory` output for the host
architecture; any `Format=` or output settings in your configuration are
overridden. The configuration should install a kernel, systemd and dracut
itself.

## Roots

A root is a copy-on-write disk over an image. Changes made in a machine
(installed packages, files outside the home) stay in the root.

```sh
toby root create NAME --image ID      # or --image default
toby root ls                          # shows roots whose image has a newer build
toby root reset NAME                  # discard all changes: back to the image
toby root rebase NAME [--image ID]    # start over from a newer image
toby root rm NAME
```

A root boots the kernel of its image. `rebase` without `--image` moves the
root to the newest image built from the same source; like `reset`, it
discards the changes made in the root.

## Homes

A home is a disk mounted at `/home/USER` in the machine. It keeps the
user's files, shell history and tool configuration across roots, resets and
rebases.

```sh
toby home create NAME [--user NAME] [--uid UID]
toby home ls
toby home rm NAME
```

The user name and ID default to yours. When a machine starts, Toby creates
the user in the root (with passwordless `sudo`), mounts the home and runs
commands as that user. The home is formatted when it is created and filled
from the image's `/etc/skel` on first use.

## Custom images

An image works with Toby if, after adaptation:

- systemd is `/sbin/init`;
- a Linux kernel with its modules and dracut are installed;
- the image is for the host's architecture;
- the tools you want to run have what they need (for example `curl`, `git`
  or `node`).

Toby adapts every image after it is built: it installs whatever of
systemd, sudo, dracut and a kernel is missing, adds its own initramfs
module, generates the initramfs, and masks units that do not make sense in
a Toby machine (console logins, first-boot setup, and network managers
such as NetworkManager or systemd-networkd, since Toby configures the
network itself).
Automatic installation supports these distributions:

| Family | Packages installed if missing |
| --- | --- |
| Debian, Ubuntu | `systemd systemd-sysv sudo dracut ca-certificates` and `linux-image-cloud-<arch>` (Debian) or `linux-virtual` (Ubuntu) |
| Fedora, RHEL | `systemd sudo dracut ca-certificates kernel-core` |
| Arch Linux | `systemd sudo dracut ca-certificates linux` |
| openSUSE | `systemd sudo dracut ca-certificates kernel-default-base` |

Images for other distributions must already provide systemd, sudo, a
kernel and dracut. A container image can carry the label
`dev.toby.adapted=manual` to make Toby never install packages into it; the
build then fails if anything is missing. Images without systemd (for
example Alpine) are not supported.

Toby needs nothing else from an image: networking, the user, the home and
shared folders are set up by Toby's own programs, which the machine reads
from the host.
