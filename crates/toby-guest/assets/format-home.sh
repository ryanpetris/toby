#!/bin/sh
# Formats a new home disk (attached with serial "out") as ext4.

set -eu
mkfs.ext4 -q -L toby-home /dev/disk/by-id/virtio-out
