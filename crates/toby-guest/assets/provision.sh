#!/bin/sh
# Prepares the stock Debian cloud image used once to build the first default
# image: installs what builds need (as root).

set -eu
export DEBIAN_FRONTEND=noninteractive
echo "==> Updating the bootstrap builder"
apt-get update -q
apt-get full-upgrade -y -q
apt-get install -y -q --no-install-recommends \
    bubblewrap buildah ca-certificates cpio debian-archive-keyring dracut dracut-core e2fsprogs \
    fuse-overlayfs git podman python3 python3-pefile systemd-container uidmap zstd
