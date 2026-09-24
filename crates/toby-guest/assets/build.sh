#!/bin/sh
# Builds a Toby image inside a builder machine (as root).
#
# usage: build.sh BUILD-ID [--pull] KIND [ARGS...]
#   default                     the bundled default image configuration
#   mkosi DIR                   an mkosi configuration (DIR inside /build/context)
#   dockerfile FILE             a Dockerfile (FILE inside /build/context)
#   registry REF [AUTHFILE]     a registry image
#   archive FILE                an OCI archive (FILE inside /build/context)
# --pull fetches a Dockerfile's base images again.
#
# Needs: the cache disk (serial "cache"), the output disk (serial "out"),
# /build/context (the context attachment, if any) and /build/boot (the
# writable directory that receives the kernel, initramfs and image config).
# Environment: TOBY_ADAPT_MANUAL=1 for images that must not be modified.

set -eu
id=$1
shift
pull=missing
if [ "$1" = --pull ]; then
    pull=always
    shift
fi
kind=$1
shift

here=$(dirname "$0")
boot=/build/boot
log() { printf '==> %s\n' "$*"; }

case $(uname -m) in
    x86_64) mkosi_arch=x86-64 ;;
    aarch64) mkosi_arch=arm64 ;;
    *) mkosi_arch=$(uname -m) ;;
esac

# The cache disk (one per image source) keeps container layers and mkosi
# caches between builds of that source.
cache=/dev/disk/by-id/virtio-cache
mkdir -p /cache
if ! mountpoint -q /cache; then
    if ! blkid "$cache" >/dev/null 2>&1; then
        log "Formatting the build cache"
        mkfs.ext4 -q -L toby-cache "$cache"
    fi
    mount "$cache" /cache
fi
mkdir -p /cache/containers /cache/mkosi/packages /cache/mkosi/cache /cache/mkosi/work /cache/mkosi/out \
    /var/lib/containers
mountpoint -q /var/lib/containers || mount --bind /cache/containers /var/lib/containers

# Cloud Hypervisor boots an arm64 kernel only as an uncompressed Image;
# distributions ship it gzip-compressed or as an EFI zboot image, whose
# header names the payload's offset, size and compression.
checked_size() {
    if [ "$(wc -c < "$1")" -ge "$max" ]; then
        echo "The unpacked kernel is larger than Toby takes" >&2
        exit 1
    fi
}

unpack_arm64_kernel() {
    f=$1
    # As much as tobyd takes from a build.
    max=536870912
    if [ "$(od -An -tx1 -N2 "$f" | tr -d ' \n')" = 1f8b ]; then
        gzip -dc "$f" | head -c "$max" > "$f.image"
        checked_size "$f.image"
        mv "$f.image" "$f"
    elif [ "$(dd if="$f" bs=1 skip=4 count=4 2>/dev/null)" = zimg ]; then
        off=$(od -An -tu4 -j8 -N4 "$f" | tr -d ' ')
        size=$(od -An -tu4 -j12 -N4 "$f" | tr -d ' ')
        comp=$(dd if="$f" bs=1 skip=24 count=8 2>/dev/null | tr -d '\0')
        case $comp in
            gzip) set -- gzip -dc ;;
            zstd*) set -- zstd -dc ;;
            xz|xzkern) set -- xz -dc ;;
            lz4) set -- lz4 -dc ;;
            lzma) set -- xz --format=lzma -dc ;;
            *) echo "The kernel is compressed with $comp, which Toby cannot unpack" >&2; exit 1 ;;
        esac
        tail -c +$((off + 1)) "$f" | head -c "$size" | "$@" | head -c "$max" > "$f.image"
        checked_size "$f.image"
        mv "$f.image" "$f"
    fi
}

container=
tree=
cleanup() {
    if [ -n "$container" ]; then
        buildah umount "$container" >/dev/null 2>&1 || true
        buildah rm "$container" >/dev/null 2>&1 || true
    fi
    # Images a newer build replaced; the cache keeps the current one's layers.
    if command -v buildah >/dev/null 2>&1; then
        buildah rmi --prune >/dev/null 2>&1 || true
    fi
    if [ "$kind" = mkosi ] || [ "$kind" = default ]; then
        rm -rf "/cache/mkosi/out/$id"
    fi
    if mountpoint -q /out; then umount /out; fi
}
trap cleanup EXIT

from_image() {
    container=$(buildah from "$1")
    tree=$(buildah mount "$container")
    buildah inspect --type image "$1" > "$boot/config.json"
    label=$(buildah inspect --type image --format '{{index .OCIv1.Config.Labels "dev.toby.adapted"}}' "$1" 2>/dev/null || true)
    if [ "$label" = manual ]; then
        export TOBY_ADAPT_MANUAL=1
    fi
}

case $kind in
    default | mkosi)
        if [ "$kind" = default ]; then conf=/run/toby/fs/images/default; else conf="/build/context/$1"; fi
        # mkosi skips a build whose output exists; drop anything an
        # interrupted build left behind.
        find /cache/mkosi/out -mindepth 1 -maxdepth 1 -exec rm -rf {} +
        # mkosi writes its tools tree next to the configuration, which is
        # read-only here; build from a writable copy that keeps the tree.
        work=/cache/mkosi/conf
        mkdir -p "$work"
        find "$work" -mindepth 1 -maxdepth 1 ! -name mkosi.tools ! -name mkosi.tools.manifest \
            ! -name mkosi.tools.build.cache -exec rm -rf {} +
        cp -a "$conf"/. "$work"/
        log "Building with mkosi"
        /run/toby/fs/mkosi/bin/mkosi -C "$work" --format=directory --architecture="$mkosi_arch" \
            --output-directory=/cache/mkosi/out --output="$id" --workspace-directory=/cache/mkosi/work \
            --incremental=yes --package-cache-directory=/cache/mkosi/packages \
            --cache-directory=/cache/mkosi/cache --tools-tree=default build
        tree="/cache/mkosi/out/$id"
        echo '{}' > "$boot/config.json"
        ;;
    dockerfile)
        log "Building the Dockerfile"
        buildah build --layers --pull="$pull" --network host -f "/build/context/$1" -t toby/build /build/context
        from_image toby/build
        ;;
    registry)
        log "Pulling $1"
        if [ -n "${2:-}" ]; then
            buildah pull --authfile "$2" "$1"
        else
            buildah pull "$1"
        fi
        from_image "$1"
        ;;
    archive)
        log "Importing $1"
        image=$(buildah pull -q "oci-archive:/build/context/$1")
        from_image "$image"
        ;;
    *)
        echo "unknown build kind $kind" >&2
        exit 2
        ;;
esac

log "Adapting the root filesystem"
sh "$here/adapt.sh" "$tree"
kver=$(cat /run/toby/build/kernel-version)

log "Exporting the image"
mkfs.ext4 -q -F -L toby-root /dev/disk/by-id/virtio-out
mkdir -p /out
mount /dev/disk/by-id/virtio-out /out
cp -a --sparse=always --one-file-system "$tree"/. /out/

kernel=
for k in "$tree/usr/lib/modules/$kver/vmlinuz" "$tree/boot/vmlinuz-$kver" "$tree/boot/vmlinuz-linux"; do
    if [ -e "$k" ]; then kernel=$k; break; fi
done
[ -n "$kernel" ] || { echo "No kernel image for $kver" >&2; exit 1; }
cp -L "$kernel" "$boot/vmlinuz"
if [ "$(uname -m)" = aarch64 ]; then
    unpack_arm64_kernel "$boot/vmlinuz"
fi
cp "$tree/boot/toby-initramfs.img" "$boot/initramfs.img"
echo "$kver" > "$boot/kernel-version"
fstrim /out || true
umount /out
sync
log "Done"
